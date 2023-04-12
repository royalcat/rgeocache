package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/geoparser"

	"github.com/fasthttp/router"
	"github.com/paulmach/orb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
	_ "go.uber.org/automaxprocs"
)

type Server struct {
	rgeo *geocoder.RGeoCoder
}

func main() {
	// f, err := os.Create("cpu.prof")
	// if err != nil {
	// 	log.Fatal("could not create CPU profile: ", err)
	// }
	// defer f.Close() // error handling omitted for example
	// if err := pprof.StartCPUProfile(f); err != nil {
	// 	log.Fatal("could not start CPU profile: ", err)
	// }
	// defer pprof.StopCPUProfile()

	app := &cli.App{
		Commands: []*cli.Command{
			{
				Name:  "serve",
				Usage: "serve a rgeocache api",
				Flags: []cli.Flag{
					&cli.PathFlag{
						Name:     "points",
						Required: true,
					},
				},
				Action: serve,
			},
			{
				Name:    "generate",
				Aliases: []string{"g"},
				Usage:   "generates a rgeocache points data",
				Flags: []cli.Flag{
					&cli.PathFlag{
						Name:     "points",
						Aliases:  []string{"p"},
						Required: true,
					},
					&cli.PathFlag{
						Name:        "cache",
						Aliases:     []string{"c"},
						Value:       "memory",
						DefaultText: "memory",
					},
					&cli.StringSliceFlag{
						Name:      "input",
						Aliases:   []string{"i"},
						TakesFile: true,
					},
					&cli.IntFlag{
						Name:        "threads",
						Aliases:     []string{"t"},
						DefaultText: "max",
					},
				},
				Action: generate,
			},
		},
	}

	if err := app.Run(os.Args); err != nil {
		logrus.Fatal(err)
	}

}

func generate(ctx *cli.Context) error {
	cache := ctx.Path("cache")
	if cache == "" {
		cache = "memory"
	}
	threads := ctx.Int("threads")
	if threads == 0 {
		threads = runtime.GOMAXPROCS(0)
	}

	geoGen, err := geoparser.NewGeoGen(cache, true, threads)
	if err != nil {
		return fmt.Errorf("error creating geoGen: %w", err)
	}
	defer geoGen.Close()

	inputs := ctx.StringSlice("input")
	for _, v := range inputs {
		err := geoGen.ParseOSMFile(v)
		if err != nil {
			return fmt.Errorf("error parsing input: %s with error: %s", v, err.Error())
		}
		err = geoGen.OpenCache() // flushing memory cache
		if err != nil {
			return fmt.Errorf("error flushing memory cache: %s", err.Error())
		}
	}

	fmt.Println("generatating complete, saving...")
	err = geoGen.SavePointsToFile(ctx.Path("points"))
	if err != nil {
		return fmt.Errorf("failed to save points to file: %s", err.Error())
	}

	return nil
}

func serve(ctx *cli.Context) error {
	srv := &Server{
		rgeo: &geocoder.RGeoCoder{},
	}
	logrus.Info("Initing geocoder")
	err := srv.rgeo.LoadFromPointsFile(ctx.Path("points"))
	if err != nil {
		return err
	}

	r := router.New()
	r.GET("/rgeocode/address/{lat}/{lon}", srv.RGeoCodeHandler)
	r.GET("/rgeocode/multiaddress", srv.RGeoMultipleCodeHandler)
	r.Handle(http.MethodGet, "/metrics", fasthttpadaptor.NewFastHTTPHandler(promhttp.Handler()))

	server := &fasthttp.Server{
		GetOnly:        true,
		ReadBufferSize: 0,
		ReadTimeout:    time.Second,
		Handler:        r.Handler,
	}
	go func() {
		if err := server.ListenAndServe(":8080"); err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe(): %v", err)
		}
	}()
	logrus.Info("Server started")

	// wait cancel
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server.ShutdownWithContext(shutdownCtx)
	return nil
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
			Name:      "call_count",
			Help:      "count of address interactions",
		},
	)
	metricHttpMultiAdressCallCount = promauto.NewCounter(
		prometheus.CounterOpts{
			Namespace: "rgeocode",
			Subsystem: "http_multi_address",
			Name:      "call_count",
			Help:      "count of address interactions",
		},
	)
)
