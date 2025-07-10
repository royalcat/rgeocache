package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	stdlog "log"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/fasthttp/router"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const MaxBodySize = 32 * 1000 * 1000 // 32MB

var meter = otel.Meter("github.com/royalcat/rgeocache/server")

func Run(ctx context.Context, address string, rgeo *geocoder.RGeoCoder) error {
	if err := setupTelemetry(ctx); err != nil {
		return fmt.Errorf("failed to initialize otel metrics: %w", err)
	}

	log := slog.Default()

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
	s := &server{
		rgeo: rgeo,

		metricHttpAddressCallCount:      metricHttpAdressCallCount,
		metricHttpAddressMultiCallCount: metricHttpAddressMultiCallCount,
		metricAddressesEncoded:          metricHttpAdressEncoded,
	}

	r := router.New()
	r.GET("/rgeocode/address/{lat}/{lon}", s.RGeoCodeHandler)
	r.GET("/rgeocode/multiaddress", s.RGeoMultipleCodeHandler) // DEPRECATED use post endpoint
	r.POST("/rgeocode/multiaddress", s.RGeoMultipleCodeHandler)
	r.Handle(http.MethodGet, "/metrics", fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler()))

	server := &fasthttp.Server{
		ReadTimeout:        time.Second,
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
	slog.Info("Server started")

	// wait cancel
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return server.ShutdownWithContext(shutdownCtx)
}

type server struct {
	rgeo *geocoder.RGeoCoder

	metricHttpAddressCallCount      metric.Int64Counter
	metricHttpAddressMultiCallCount metric.Int64Counter
	metricAddressesEncoded          metric.Int64Counter
}

var reqPointsPool = sync.Pool{
	New: func() any {
		return [][2]float64{}
	},
}

var bufPool = sync.Pool{
	New: func() any {
		return &bytes.Buffer{}
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
	for _, p := range req {
		info, _ := s.rgeo.Find(p[0], p[1])
		res = append(res, info.Info)
	}

	data, err := res.MarshalJSON()
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(data)
}
