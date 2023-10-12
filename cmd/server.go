package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/fasthttp/router"
	"github.com/paulmach/orb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/sirupsen/logrus"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

type Server struct {
	rgeo *geocoder.RGeoCoder
}

func (s *Server) ListenAndServe(ctx context.Context, address string) error {
	log := logrus.New()

	r := router.New()
	r.GET("/rgeocode/address/{lat}/{lon}", s.RGeoCodeHandler)
	r.GET("/rgeocode/multiaddress", s.RGeoMultipleCodeHandler) // DEPRECATED use post endpoint
	r.POST("/rgeocode/multiaddress", s.RGeoMultipleCodeHandler)
	r.Handle(http.MethodGet, "/metrics", fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler()))

	server := &fasthttp.Server{
		GetOnly:     true,
		ReadTimeout: time.Second,
		Handler:     r.Handler,
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

func (s *Server) RGeoCodeHandler(ctx *fasthttp.RequestCtx) {
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

	body, err := json.Marshal(i)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.BodyWriter().Write(body)
	return
}

func (s *Server) RGeoMultipleCodeHandler(ctx *fasthttp.RequestCtx) {
	metricHttpMultiAdressCallCount.Inc()

	req := []orb.Point{} // latitude, longitude
	err := json.Unmarshal(ctx.Request.Body(), &req)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusBadRequest)
		return
	}

	res := []geomodel.Info{}
	for _, p := range req {
		info, _ := s.rgeo.Find(p[0], p[1])
		res = append(res, info.Info)
	}

	body, err := json.Marshal(res)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.BodyWriter().Write(body)
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
