package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/fasthttp/router"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

const MaxBodySize = 32 * 1000 * 1000 // 32MB

func Run(ctx context.Context, address string, rgeo *geocoder.RGeoCoder) error {
	log := logrus.New()

	s := &server{
		rgeo: rgeo,
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
		Logger:             logrus.NewEntry(log).WithField("component", "fasthttp"),
	}

	go func() {
		log.Infof("Server listening on: %s", address)
		if err := server.ListenAndServe(address); err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe(): %v", err)
		}
	}()
	logrus.Info("Server started")

	// wait cancel
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return server.ShutdownWithContext(shutdownCtx)
}

type server struct {
	rgeo *geocoder.RGeoCoder
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
	metricHttpAdressCallCount.Inc()

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

	return
}

func (s *server) RGeoMultipleCodeHandler(ctx *fasthttp.RequestCtx) {
	metricHttpMultiAdressCallCount.Inc()

	req := reqPointsPool.Get().([][2]float64) // lat, lon
	req = req[:0]
	defer reqPointsPool.Put(req)

	err := json.Unmarshal(ctx.Request.Body(), &req)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		ctx.Response.SetBodyString("failed to parse request: " + err.Error())
		return
	}

	res := []geomodel.Info{}
	for _, p := range req {
		info, _ := s.rgeo.Find(p[0], p[1])
		res = append(res, info.Info)
	}

	data, err := json.Marshal(res)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(data)
	return
}

var (
	metricHttpAdressCallCount = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "rgeocode",
			Subsystem: "http_address",
			Name:      "call_count_total",
			Help:      "count of address interactions",
		},
	)
	metricHttpMultiAdressCallCount = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "rgeocode",
			Subsystem: "http_multi_address",
			Name:      "call_count_total",
			Help:      "count of address interactions",
		},
	)
)
