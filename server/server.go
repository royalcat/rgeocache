package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/router"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/royalcat/rgeocache/fgeocode"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const MaxBodySize = 32 * 1000 * 1000 // 32MB

var meter = otel.Meter("github.com/royalcat/rgeocache/server")

func Run(ctx context.Context, address string, rgeo geocoder.Geocoder, fgeo *fgeocode.Geocoder, pointsPerThread int, log *slog.Logger) error {
	if err := setupTelemetry(ctx); err != nil {
		return fmt.Errorf("failed to initialize otel metrics: %w", err)
	}

	if fgeo != nil {
		go fgeo.Build(ctx)
	}

	metricHttpAdressCallCount, err := meter.Int64Counter("http_address_call_total")
	if err != nil {
		return err
	}
	metricHttpAddressMultiCallCount, err := meter.Int64Counter("http_address_multi_call_total")
	if err != nil {
		return err
	}
	metricHttpAdressEncoded, err := meter.Int64Counter("address_encoded_total")
	if err != nil {
		return err
	}
	metricFGeoCallCount, err := meter.Int64Counter("fgeocode_call_total")
	if err != nil {
		return err
	}
	metricFGeoAutocompleteCallCount, err := meter.Int64Counter("fgeocode_autocomplete_call_total")
	if err != nil {
		return err
	}
	s := &server{
		rgeo:            rgeo,
		fgeo:            fgeo,
		pointsPerThread: int(pointsPerThread),

		metricHttpAddressCallCount:      metricHttpAdressCallCount,
		metricHttpAddressMultiCallCount: metricHttpAddressMultiCallCount,
		metricAddressesEncoded:          metricHttpAdressEncoded,
		metricFGeoCallCount:             metricFGeoCallCount,
		metricFGeoAutocompleteCallCount: metricFGeoAutocompleteCallCount,
	}

	r := router.New()
	r.GET("/rgeocode/address/{lat}/{lon}", s.RGeoCodeHandler)
	r.GET("/rgeocode/multiaddress", s.RGeoMultipleCodeHandler) // DEPRECATED use post endpoint
	r.POST("/rgeocode/multiaddress", s.RGeoMultipleCodeHandler)
	r.GET("/fgeocode/search", s.FGeoCodeHandler)
	r.GET("/fgeocode/autocomplete", s.FGeoAutocompleteHandler)
	r.Handle(http.MethodGet, "/metrics", fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler()))

	server := &fasthttp.Server{
		ReadTimeout:        time.Second * 30,
		MaxRequestBodySize: MaxBodySize,
		Handler:            r.Handler,
		// Logger:             logrus.NewEntry(log).WithField("component", "fasthttp"),
	}

	go func() {
		log.Info("Server listening", "address", address)
		if err := server.ListenAndServe(address); err != http.ErrServerClosed {
			stdlog.Fatalf("ListenAndServe(): %v", err)
		}
	}()
	log.Info("Server started")

	// wait cancel
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return server.ShutdownWithContext(shutdownCtx)
}

type server struct {
	rgeo            geocoder.Geocoder
	fgeo            *fgeocode.Geocoder
	pointsPerThread int

	metricHttpAddressCallCount      metric.Int64Counter
	metricHttpAddressMultiCallCount metric.Int64Counter
	metricAddressesEncoded          metric.Int64Counter
	metricFGeoCallCount             metric.Int64Counter
	metricFGeoAutocompleteCallCount metric.Int64Counter
}

var reqPointsPool = sync.Pool{
	New: func() any {
		return [][2]float64{}
	},
}

func (s *server) RGeoCodeHandler(ctx *fasthttp.RequestCtx) {
	s.metricHttpAddressCallCount.Add(ctx, 1)
	s.metricAddressesEncoded.Add(ctx, 1)

	latS := ctx.UserValue("lat").(string)
	lonS := ctx.UserValue("lon").(string)

	lat, err := strconv.ParseFloat(latS, 64)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		return
	}
	lon, err := strconv.ParseFloat(lonS, 64)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		return
	}

	i, ok := s.rgeo.Find(lat, lon)
	if !ok {
		ctx.Response.SetStatusCode(http.StatusNoContent)
		return
	}

	out, err := json.Marshal(i)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		ctx.Response.SetBodyString("failed to marshal response")
	}

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(out)
}

func (s *server) RGeoMultipleCodeHandler(ctx *fasthttp.RequestCtx) {
	s.metricHttpAddressMultiCallCount.Add(ctx, 1)

	req := reqPointsPool.Get().([][2]float64) // lat, lon
	req = req[:0]
	defer reqPointsPool.Put(req)

	err := json.Unmarshal(ctx.Request.Body(), &req)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("failed to parse request: " + err.Error())
		return
	}

	s.metricAddressesEncoded.Add(ctx, int64(len(req)))

	res := geomodel.InfoList{}

	if len(req) < s.pointsPerThread {
		for _, p := range req {
			info, _ := s.rgeo.Find(p[0], p[1])
			res = append(res, info.Info)
		}
	} else {
		threads := min(max(2, len(req)/s.pointsPerThread), runtime.GOMAXPROCS(0)/2)
		res = s.multithreadedFind(req, threads)
	}

	data, err := res.MarshalJSON()
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(data)
}

func (s *server) multithreadedFind(points [][2]float64, threads int) []geomodel.Info {
	var res = make([]geomodel.Info, len(points))
	var taskChan = make(chan int, threads)

	go func() {
		for i := range points {
			taskChan <- i
		}
		close(taskChan)
	}()

	var wg sync.WaitGroup
	wg.Add(threads)
	for range threads {
		go func() {
			for i := range taskChan {
				info, _ := s.rgeo.Find(points[i][0], points[i][1])
				res[i] = info.Info
			}
			wg.Done()
		}()
	}
	wg.Wait()
	return res
}

// FGeoCodeHandler implements GET /fgeocode/search, feature-compatible with the
// Rust server: same parameters, response shape, validation limits and 503
// handling while the index builds.
func (s *server) FGeoCodeHandler(ctx *fasthttp.RequestCtx) {
	s.metricFGeoCallCount.Add(ctx, 1)

	if s.fgeo == nil {
		ctx.Response.SetStatusCode(http.StatusServiceUnavailable)
		ctx.Response.SetBodyString("forward geocoding is disabled")
		return
	}
	if err := s.fgeo.Ready(); err != nil {
		fgeoUnavailable(ctx, err)
		return
	}

	args := ctx.QueryArgs()
	q := string(args.Peek("q"))
	if len(q) > fgeocode.MaxQueryLen {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("query too long")
		return
	}

	req := fgeocode.SearchRequest{
		Query:          q,
		City:           string(args.Peek("city")),
		Region:         string(args.Peek("region")),
		Street:         string(args.Peek("street")),
		House:          string(args.Peek("house")),
		Name:           string(args.Peek("name")),
		Kinds:          fgeocode.ParseKindFilter(string(args.Peek("kind"))),
		IncludePolygon: true,
	}
	for _, value := range []string{req.City, req.Region, req.Street, req.House, req.Name} {
		if len(value) > fgeocode.MaxQueryLen {
			ctx.Response.SetStatusCode(http.StatusBadRequest)
			ctx.Response.SetBodyString("structured field too long")
			return
		}
	}
	if strings.TrimSpace(req.Query) == "" &&
		req.City == "" && req.Region == "" && req.Street == "" && req.House == "" && req.Name == "" {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("q or a structured field is required")
		return
	}

	limit, err := queryInt(args, "limit", fgeocode.DefaultLimit)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("invalid limit")
		return
	}
	req.Limit = limit

	offset, err := queryInt(args, "offset", 0)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("invalid offset")
		return
	}
	req.Offset = offset

	if args.Has("include_polygon") {
		req.IncludePolygon = args.GetBool("include_polygon")
	}

	results, err := s.fgeo.Search(req)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		ctx.Response.SetBodyString("forward search failed: " + err.Error())
		return
	}

	body, err := json.Marshal(fgeocode.SearchResponse{Results: results})
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}
	ctx.Response.Header.SetContentType("application/json")
	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(body)
}

// FGeoAutocompleteHandler implements GET /fgeocode/autocomplete.
func (s *server) FGeoAutocompleteHandler(ctx *fasthttp.RequestCtx) {
	s.metricFGeoAutocompleteCallCount.Add(ctx, 1)

	if s.fgeo == nil {
		ctx.Response.SetStatusCode(http.StatusServiceUnavailable)
		ctx.Response.SetBodyString("forward geocoding is disabled")
		return
	}
	if err := s.fgeo.Ready(); err != nil {
		fgeoUnavailable(ctx, err)
		return
	}

	args := ctx.QueryArgs()
	q := string(args.Peek("q"))
	if strings.TrimSpace(q) == "" {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("q is required")
		return
	}
	if len(q) > fgeocode.MaxQueryLen {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("query too long")
		return
	}

	limit, err := queryInt(args, "limit", fgeocode.DefaultLimit)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("invalid limit")
		return
	}

	suggestions, err := s.fgeo.Suggest(q, limit)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		ctx.Response.SetBodyString("forward autocomplete failed: " + err.Error())
		return
	}

	body, err := json.Marshal(fgeocode.AutocompleteResponse{Suggestions: suggestions})
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}
	ctx.Response.Header.SetContentType("application/json")
	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(body)
}

// fgeoUnavailable writes the 503 used while the forward index builds or after
// a failed build.
func fgeoUnavailable(ctx *fasthttp.RequestCtx, err error) {
	ctx.Response.SetStatusCode(http.StatusServiceUnavailable)
	if errors.Is(err, fgeocode.ErrBuilding) {
		ctx.Response.Header.Set("Retry-After", "5")
		ctx.Response.SetBodyString("forward geocoder index is still building")
		return
	}
	ctx.Response.SetBodyString("forward geocoder unavailable: " + err.Error())
}

func queryInt(args *fasthttp.Args, key string, defaultValue int) (int, error) {
	if !args.Has(key) {
		return defaultValue, nil
	}
	value, err := args.GetUint(key)
	if err != nil {
		return 0, err
	}
	return int(value), nil
}
