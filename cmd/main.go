package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fasthttp/router"
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
						Value:       memoryCache,
						DefaultText: "memory",
						Usage:       "cache type, can be 'memory', 'temp' or path to a directory",
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
						Name:        "pprof.listen",
						DefaultText: "",
					},
					&cli.BoolFlag{
						Name:        "pprof.profile",
						DefaultText: "",
					},
					&cli.BoolFlag{
						Name:        "pprof.heap",
						DefaultText: "",
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

const memoryCache = "memory"

func generate(ctx *cli.Context) error {
	log := logrus.NewEntry(logrus.StandardLogger())
	// cache := ctx.String("cache")
	// if cache == "" {
	// 	cache = memoryCache
	// }
	// if cache == "temp" {
	// 	tempDir, err := os.MkdirTemp("", "rgeocache")
	// 	if err != nil {
	// 		return err
	// 	}
	// 	log.Infof("Using dir %s as cache", tempDir)
	// 	defer os.RemoveAll(tempDir)
	// 	cache = tempDir
	// }
	// log = log.WithField("cache", cache)

	threads := ctx.Int("threads")
	if threads == 0 {
		threads = runtime.GOMAXPROCS(0)
	}
	log = log.WithField("threads", threads)

	preferredLocalization := ctx.String("preferred-localization")
	if preferredLocalization == "official" {
		preferredLocalization = ""
	}

	if pprofListen := ctx.String("pprof.listen"); pprofListen != "" {
		go func() {
			logrus.Info("Starting pprof server")
			logrus.Error(http.ListenAndServe(pprofListen, nil))
		}()
	}

	pprofHeap := ctx.Bool("pprof.heap")

	if ctx.Bool("pprof.profile") {
		f, err := os.OpenFile("profile.pprof", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("error creating pprof file: %w", err)
		}
		err = pprof.StartCPUProfile(f)
		if err != nil {
			return fmt.Errorf("error starting pprof: %w", err)
		}
		defer pprof.StopCPUProfile()
	}

	geoGen, err := geoparser.NewGeoGen(threads, preferredLocalization)
	if err != nil {
		return fmt.Errorf("error creating geoGen: %w", err)
	}

	inputs := ctx.StringSlice("input")
	fmt.Printf("Input maps: %v\n", inputs)
	for _, input := range inputs {
		fmt.Printf("Generating database for map: %s\n", input)
		err := geoGen.ParseOSMFile(ctx.Context, input)
		if err != nil {
			return fmt.Errorf("error parsing input: %s with error: %s", input, err.Error())
		}

		if pprofHeap {
			err := writeHeapProfile(path.Base(input))
			if err != nil {
				return fmt.Errorf("error writing heap profile: %s", err.Error())
			}
		}

		err = geoGen.ResetCache()
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

func writeHeapProfile(name string) error {
	f, err := os.Create(name + ".heap.prof")
	if err != nil {
		logrus.Fatal(err)
	}
	defer f.Close()
	return pprof.WriteHeapProfile(f)
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

const MaxBodySize = 32 * 1000 * 1000 // 32MB

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
		ReadTimeout:        time.Second,
		MaxRequestBodySize: MaxBodySize,
		Handler:            r.Handler,
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

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	writeGeoInfoFast(buf, i.Info)

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(buf.Bytes())

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

	// err := unmarshalPointsListFast(ctx.Request.Body(), &req)
	// if err != nil {
	// 	ctx.Response.SetStatusCode(http.StatusBadRequest)
	// 	ctx.Response.SetBodyString("failed to parse request: " + err.Error())
	// 	return
	// }

	res := []geomodel.Info{}
	for _, p := range req {
		info, _ := s.rgeo.Find(p[0], p[1])
		res = append(res, info.Info)
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)

	writeGeoInfoListFast(buf, res)

	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(buf.Bytes())
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

func unmarshalPointsListFast(data []byte, result *[][2]float64) error {
	i := 0
	n := len(data)

	*result = slices.Grow(*result, n/16) // n/16 is a heuristic

	// Skip leading whitespace
	for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
		i++
	}

	if i >= n || data[i] != '[' {
		return fmt.Errorf("invalid format: expected '['")
	}
	i++

	for i < n {
		// Skip whitespace
		for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
			i++
		}

		if i < n && data[i] == ']' {
			i++
			break
		}

		if i >= n || data[i] != '[' {
			return fmt.Errorf("invalid format: expected '[' for point")
		}
		i++

		var point [2]float64
		for j := 0; j < 2; j++ {
			// Skip whitespace
			for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
				i++
			}

			start := i
			// Find the end of the number
			for i < n && ((data[i] >= '0' && data[i] <= '9') || data[i] == '-' || data[i] == '.' || data[i] == 'e' || data[i] == 'E') {
				i++
			}
			if start == i {
				point[j] = 0
			} else {
				num, err := strconv.ParseFloat(string(data[start:i]), 64)
				if err != nil {
					return fmt.Errorf("invalid number: %v", err)
				}
				point[j] = num
			}

			// Skip whitespace
			for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
				i++
			}

			if j < 1 {
				if i < n && data[i] == ',' {
					i++
				} else {
					return fmt.Errorf("invalid format: expected ',' between coordinates")
				}
			}
		}

		// After two numbers, skip to end of point
		for i < n && data[i] != ']' {
			i++
		}
		if i >= n || data[i] != ']' {
			return fmt.Errorf("invalid format: expected ']' at end of point")
		}
		i++

		*result = append(*result, point)

		// Skip whitespace
		for i < n && (data[i] == ' ' || data[i] == '\n' || data[i] == '\t' || data[i] == '\r') {
			i++
		}

		// Check for comma or end
		if i < n && data[i] == ',' {
			i++
			continue
		} else if i < n && data[i] == ']' {
			i++
			break
		}
	}

	return nil
}

func writeGeoInfoFast(buf *bytes.Buffer, i geomodel.Info) {
	// return []byte(fmt.Sprintf(`{"region":"%s","city":"%s","street":"%s","house_number":"%s"}`, p.Region, p.City, p.Street, p.HouseNumber))
	buf.WriteString("{")
	buf.WriteString(`"region":"`)
	buf.WriteString(i.Region)
	buf.WriteString(`","city":"`)
	buf.WriteString(i.City)
	buf.WriteString(`","street":"`)
	buf.WriteString(i.Street)
	buf.WriteString(`","house_number":"`)
	buf.WriteString(i.HouseNumber)
	buf.WriteString(`"}`)
}

func writeGeoInfoListFast(buf *bytes.Buffer, infos []geomodel.Info) {
	buf.WriteRune('[')
	for i, v := range infos {
		writeGeoInfoFast(buf, v)
		if i != len(infos)-1 {
			buf.WriteRune(',')
		}

	}
	buf.WriteByte(']')
}
