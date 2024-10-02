package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/paulmach/orb"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/geoparser"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"

	_ "net/http/pprof"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
	_ "go.uber.org/automaxprocs"
)

func main() {
	app := &cli.App{
		Name:        "rgeocache",
		Description: "Reverse geocoder with pregenerated cache",
		Commands: []*cli.Command{
			{
				Name:  "serve",
				Usage: "serve a rgeocache api",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:      "points",
						Aliases:   []string{"p"},
						Required:  true,
						TakesFile: true,
					},
					&cli.StringFlag{
						Name:  "listen",
						Value: ":8080",
					},
				},
				Action: serve,
			},
			{
				Name:    "generate",
				Aliases: []string{"g"},
				Usage:   "generates a rgeocache points data",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:      "points",
						Aliases:   []string{"p"},
						Required:  true,
						TakesFile: true,
					},
					&cli.StringFlag{
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
					&cli.StringFlag{
						Name:        "preferred-localization",
						Aliases:     []string{"l"},
						DefaultText: "official",
						Value:       "official",
					},
					&cli.StringFlag{
						Name:        "pprof",
						DefaultText: "",
						Value:       "",
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
	cache := ctx.String("cache")
	if cache == "" {
		cache = "memory"
	}
	threads := ctx.Int("threads")
	if threads == 0 {
		threads = runtime.GOMAXPROCS(0)
	}
	preferredLocalization := ctx.String("preferred-localization")
	if preferredLocalization == "official" {
		preferredLocalization = ""
	}

	if profileName := ctx.String("pprof"); profileName != "" {
		// go func() {
		// 	logrus.Info("Starting pprof server")
		// 	logrus.Error(http.ListenAndServe("localhost:6060", nil))
		// }()
		f, err := os.OpenFile(profileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("error creating pprof file: %w", err)
		}
		err = pprof.StartCPUProfile(f)
		if err != nil {
			return fmt.Errorf("error starting pprof: %w", err)
		}
		defer pprof.StopCPUProfile()
	}

	geoGen, err := geoparser.NewGeoGen(cache, threads, preferredLocalization)
	if err != nil {
		return fmt.Errorf("error creating geoGen: %w", err)
	}
	defer geoGen.Close()

	inputs := ctx.StringSlice("input")
	fmt.Printf("Input maps: %v\n", inputs)
	for _, v := range inputs {
		fmt.Printf("Generating database for map: %s\n", v)
		err := geoGen.ParseOSMFile(ctx.Context, v)
		if err != nil {
			return fmt.Errorf("error parsing input: %s with error: %s", v, err.Error())
		}
		err = geoGen.OpenCache() // flushing memory cache
		if err != nil {
			return fmt.Errorf("error flushing memory cache: %s", err.Error())
		}
	}

	saveFile := ctx.String("points")
	if !strings.HasSuffix(saveFile, ".gob") {
		saveFile = saveFile + ".gob"
	}

	fmt.Printf("Generataion complete\n")
	fmt.Printf("Saving to file: %s\n", saveFile)
	err = geoGen.SavePointsToFile(saveFile)
	if err != nil {
		return fmt.Errorf("failed to save points to file: %s", err.Error())
	}

	fmt.Printf("Complete")

	return nil
}

func serve(ctx *cli.Context) error {
	log := logrus.New()
	log.Info("Initing geocoder")
	rgeo := &geocoder.RGeoCoder{}
	err := rgeo.LoadFromPointsFile(ctx.String("points"))
	if err != nil {
		return err
	}

	return runServer(ctx.Context, ctx.String("listen"), rgeo)
}

func runServer(ctx context.Context, address string, rgeo *geocoder.RGeoCoder) error {
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

type server struct {
	rgeo *geocoder.RGeoCoder
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

	body, err := json.Marshal(i)
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.BodyWriter().Write(body)
	return
}

func (s *server) RGeoMultipleCodeHandler(ctx *fasthttp.RequestCtx) {
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
