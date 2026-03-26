package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/KimMachineGun/automemlimit/memlimit"
	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geoparser"
	"github.com/royalcat/rgeocache/internal/stats"
	"github.com/royalcat/rgeocache/internal/telemetry"
	"github.com/royalcat/rgeocache/server"
	"golang.org/x/exp/mmap"

	_ "net/http/pprof"

	"github.com/urfave/cli/v3"
	_ "go.uber.org/automaxprocs"
)

func main() {
	app := &cli.Command{
		Name:                  "rgeocache",
		Description:           "Reverse geocoder with pregenerated cache",
		EnableShellCompletion: true,
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
					&cli.Float64Flag{
						Name:        "search-radius",
						Usage:       "search radius in degrees",
						DefaultText: "0.01",
					},
					&cli.StringFlag{
						Name:        "pprof.listen",
						DefaultText: "",
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
					&cli.IntFlag{
						Name:        "version",
						Aliases:     []string{},
						DefaultText: "1",
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
					&cli.StringFlag{
						Name:        "otel.endpoint",
						DefaultText: "",
					},
					&cli.StringFlag{
						Name:        "stats",
						Usage:       "Path to save runtime stats (enables stats collection when set)",
						DefaultText: "",
					},
					&cli.IntFlag{
						Name:        "stats.interval",
						Usage:       "Stats collection interval in milliseconds",
						Value:       60000,
						DefaultText: "60000",
					},
				},
				Action: generate,
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}

}

func generate(ctx context.Context, cmd *cli.Command) error {
	telemetryClient, err := telemetry.Setup(ctx, "rgeocache", cmd.String("otel.endpoint"))
	if err != nil {
		return fmt.Errorf("error setting up telemetry: %w", err)
	}
	if telemetryClient != nil {
		defer telemetryClient.Shutdown(context.Background())
	}

	log := slog.Default()

	// Setup stats collection if enabled
	statsFile := cmd.String("stats")
	var statsCollector *stats.Collector
	if statsFile != "" {
		interval := time.Duration(cmd.Int("stats.interval")) * time.Millisecond
		var err error
		statsCollector, err = stats.NewCollector(interval)
		if err != nil {
			log.Warn("Failed to create stats collector", "error", err)
		} else {
			log.Info("Starting runtime stats collection", "interval", interval, "output", statsFile)
			statsCollector.Start()
			defer func() {
				runtimeStats := statsCollector.Stop()
				log.Info("Saving runtime stats", "file", statsFile,
					"elapsed", runtimeStats.ElapsedHuman,
					"peak_heap_mb", runtimeStats.Summary.PeakHeapAlloc/(1024*1024),
					"peak_rss_mb", runtimeStats.Summary.PeakProcessRSS/(1024*1024),
					"avg_cpu_percent", runtimeStats.Summary.AvgCPUPercent)
				if err := runtimeStats.SaveToFile(statsFile); err != nil {
					log.Error("Failed to save runtime stats", "error", err)
				}
			}()
		}
	}

	threads := cmd.Int("threads")
	if threads == 0 {
		threads = runtime.GOMAXPROCS(0)
	}
	log = log.With("threads", threads)

	preferredLocalization := cmd.String("preferred-localization")
	if preferredLocalization == "official" {
		preferredLocalization = ""
	}

	version := cmd.Int("version")

	if pprofListen := cmd.String("pprof.listen"); pprofListen != "" {
		go func() {
			log.Info("Starting pprof server")
			err := http.ListenAndServe(pprofListen, nil)
			if err != nil {
				log.Error("Error starting pprof server", "error", err)
			}
		}()
	}

	pprofHeap := cmd.Bool("pprof.heap")

	if cmd.Bool("pprof.profile") {
		f, err := os.OpenFile("profile.cpu.pprof", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return fmt.Errorf("error creating pprof file: %w", err)
		}
		err = pprof.StartCPUProfile(f)
		if err != nil {
			return fmt.Errorf("error starting pprof: %w", err)
		}
		defer pprof.StopCPUProfile()
	}

	inputs := cmd.StringSlice("input")
	log.Info("Input maps", "maps", inputs)

	inputsReaders := make([]io.ReaderAt, 0, len(inputs))
	for _, input := range inputs {
		file, err := mmap.Open(input)
		if err != nil {
			return err
		}
		defer file.Close()
		inputsReaders = append(inputsReaders, file)
	}

	saveFilePath := cmd.String("points")
	if !strings.HasSuffix(saveFilePath, ".rgc") {
		saveFilePath = saveFilePath + ".rgc"
	}

	outputFile, err := os.OpenFile(saveFilePath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, os.ModePerm)
	if err != nil {
		return err
	}
	defer outputFile.Close()

	log.Info("Tuning gc to respect only soft mem limit")
	err = tuneGC()
	if err != nil {
		log.Error("Error tuning gc", "error", err)
	}

	osmdb, err := osmpbfdb.OpenMultiDB(inputsReaders, osmpbfdb.Config{
		SkipInfo:  true,
		CacheType: osmpbfdb.CacheTypeWeak,
	})
	if err != nil {
		return err
	}

	config := geoparser.ConfigDefault()
	config.PreferredLocalization = preferredLocalization
	config.Version = uint32(version)

	geoGen, err := geoparser.NewGeoGen(osmdb, config, outputFile)
	if err != nil {
		return fmt.Errorf("error creating geoGen: %w", err)
	}

	log.Info("Creating OSM cache", "output", saveFilePath)

	err = geoGen.ParseOSMData()
	if err != nil {
		return fmt.Errorf("error parsing osm with error: %s", err.Error())
	}

	if pprofHeap {
		err := writeHeapProfile("profile")
		if err != nil {
			return fmt.Errorf("error writing heap profile: %s", err.Error())
		}
	}

	log.Info("Complete")

	if telemetryClient != nil {
		err = telemetryClient.Flush(context.Background())
		if err != nil {
			log.Error("error flushing telemetry", "error", err)
		}
	}

	return nil
}

func writeHeapProfile(name string) error {
	f, err := os.Create(name + ".heap.pprof")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	return pprof.WriteHeapProfile(f)
}

const defaultSearchRadius = 0.01

func serve(ctx context.Context, cmd *cli.Command) error {
	log := slog.Default()

	if pprofListen := cmd.String("pprof.listen"); pprofListen != "" {
		go func() {
			log.Info("Starting pprof server")
			err := http.ListenAndServe(pprofListen, nil)
			if err != nil {
				log.Error("Error starting pprof server", "error", err)
			}
		}()
	}

	radius := cmd.Float64("search-radius")
	if radius <= 0 || radius > 180 {
		log.Error("Invalid radius detected using default", "input", radius, "default", 0.01)
		radius = defaultSearchRadius
	} else if radius != defaultSearchRadius {
		log.Info("Using custom search radius", "radius", radius)
	}

	cacheFile := cmd.String("points")

	err := geocoder.PrintCacheSizeAnalysisForFile(cacheFile)
	if err != nil {
		log.Error("Failed to analyze cache file", "error", err)
	}

	rgeo, err := geocoder.LoadGeoCoderFromFile(cacheFile, geocoder.WithLogger(log), geocoder.WithSearchRadius(radius))
	if err != nil {
		return err
	}

	runtime.GC()

	return server.Run(ctx, cmd.String("listen"), rgeo, log)
}

func tuneGC() error {
	_, err := memlimit.SetGoMemLimitWithOpts(
		memlimit.WithRatio(0.5),
		memlimit.WithProvider(
			memlimit.ApplyFallback(
				memlimit.FromCgroup,
				memlimit.FromSystem,
			),
		),
		memlimit.WithLogger(slog.Default()),
	)
	if err != nil {
		return fmt.Errorf("error setting memory limit: %s", err.Error())
	}

	debug.SetGCPercent(-1)
	return nil
}
