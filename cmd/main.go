package main

import (
	"context"
	"encoding/binary"
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
	"github.com/royalcat/rgeocache/cachesaver"
	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/fgeocode"
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
					&cli.Int64Flag{
						Name:        "points-per-thread",
						Usage:       "points per thread",
						DefaultText: "1000",
						Value:       1000,
					},
					&cli.StringFlag{
						Name:        "pprof.listen",
						DefaultText: "",
					},
					&cli.StringFlag{
						Name:  "listen",
						Value: ":8080",
					},
					&cli.BoolFlag{
						Name:        "fgeocode",
						Value:       true,
						Usage:       "enable forward (text) geocoding; the index is built in the background",
						DefaultText: "true",
					},
					&cli.StringFlag{
						Name:      "fgeocode-index",
						TakesFile: true,
						Usage:     "directory to persist the forward geocoding index in (reused until the cache changes); a temporary directory is used when empty",
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
						Name: "output",
						Aliases: []string{
							"o",
							"points", "p", // legacy naming
						},
						Required:  false,
						TakesFile: true,
					},
					&cli.StringFlag{
						Name:      "output-v2",
						Aliases:   []string{"o-v2"},
						Required:  false,
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
					&cli.StringFlag{
						Name:      "spool-dir",
						TakesFile: true,
						Usage:     "directory for the on-disk generation spool (defaults to <output-v2>.spool; removed on success)",
					},
				},
				Action: generate,
			},
			{
				Name:  "convert",
				Usage: "convert a v2 cache into the legacy v1 format",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:      "points",
						Aliases:   []string{"p", "input"},
						Required:  true,
						TakesFile: true,
					},
					&cli.StringFlag{
						Name:      "output",
						Aliases:   []string{"o"},
						Required:  true,
						TakesFile: true,
					},
				},
				Action: convertV1,
			},
			{
				Name: "analyze",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:      "points",
						Aliases:   []string{"p"},
						Required:  true,
						TakesFile: true,
					},
				},
				Action: analyze,
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}

}

func analyze(ctx context.Context, cmd *cli.Command) error {
	// log := slog.Default()
	// points :=
	return geocoder.PrintCacheSizeAnalysisForFile(cmd.String("points"))
}

func generate(ctx context.Context, cmd *cli.Command) error {
	if !cmd.IsSet("output") && !cmd.IsSet("output-v2") {
		return fmt.Errorf("either 'output' or 'output-v2' must be set")
	}

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

	v1Path := normalizeCachePath(cmd.String("output"))
	v2Path := normalizeCachePath(cmd.String("output-v2"))
	if v1Path == "" && v2Path == "" {
		return fmt.Errorf("either 'output' or 'output-v2' must be set")
	}
	removeTempV2 := false
	if v2Path == "" {
		// v1 is produced from a v2 cache, so a v1-only request writes a
		// temporary v2 next to the output and removes it afterwards.
		v2Path = v1Path + ".v2.tmp.rgc"
		removeTempV2 = true
		defer func() { _ = os.Remove(v2Path) }()
	}
	spoolDir := cmd.String("spool-dir")
	if spoolDir == "" {
		// Default to disk next to the output: /tmp may be a memory-backed
		// filesystem, and the spool is one to ten gigabytes.
		spoolDir = v2Path + ".spool"
	}

	if pprofListen := cmd.String("pprof.listen"); pprofListen != "" {
		go func() {
			log.Info("Starting pprof server", "address", pprofListen)
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

	log.Info("Tuning GC for bounded batch generation")
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

	cacheFormat := cmd.String("cache-format")
	if cacheFormat == "v2" {
		cacheFormat = "2"
	}

	config := geoparser.ConfigDefault()
	config.Threads = threads
	config.PreferredLocalization = preferredLocalization
	config.Version = uint32(version)
	config.SpoolDir = spoolDir

	geoGen, err := geoparser.NewGeoGen(osmdb, config)
	if err != nil {
		return fmt.Errorf("error creating geoGen: %w", err)
	}

	log.Info("Creating OSM cache")
	log.Info("Saving cache in v2 format", "path", v2Path)
	v2File, err := os.OpenFile(v2Path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, os.ModePerm)
	if err != nil {
		return err
	}
	err = geoGen.ParseOSMData([]geoparser.ParseOutput{{Format: "v2", Writer: v2File}})
	if closeErr := v2File.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("error parsing osm with error: %s", err.Error())
	}

	if pprofHeap {
		err := writeHeapProfile("profile")
		if err != nil {
			return fmt.Errorf("error writing heap profile: %s", err.Error())
		}
	}

	if v1Path != "" {
		debug.FreeOSMemory()
		log.Info("Converting cache to v1 format", "path", v1Path)
		v1File, err := os.OpenFile(v1Path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, os.ModePerm)
		if err != nil {
			return err
		}
		err = cachesaver.ConvertV2ToV1(v2Path, v1File)
		if closeErr := v1File.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return fmt.Errorf("error converting cache to v1: %w", err)
		}
	}

	if removeTempV2 {
		if err := os.Remove(v2Path); err != nil && !os.IsNotExist(err) {
			log.Warn("Failed to remove temporary v2 cache", "path", v2Path, "error", err)
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

func normalizeCachePath(path string) string {
	if path == "" {
		return ""
	}
	if !strings.HasSuffix(path, ".rgc") {
		return path + ".rgc"
	}
	return path
}

func convertV1(_ context.Context, cmd *cli.Command) error {
	out, err := os.OpenFile(cmd.String("output"), os.O_CREATE|os.O_TRUNC|os.O_RDWR, os.ModePerm)
	if err != nil {
		return err
	}
	err = cachesaver.ConvertV2ToV1(cmd.String("points"), out)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	return err
}

const defaultSearchRadius = 0.01

func serve(ctx context.Context, cmd *cli.Command) error {
	log := slog.Default()

	if pprofListen := cmd.String("pprof.listen"); pprofListen != "" {
		go func() {
			log.Info("Starting pprof server", "address", pprofListen)
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

	pointsPerThread := cmd.Int("points-per-thread")
	if pointsPerThread <= 0 {
		pointsPerThread = 1000
	}

	cacheFile := cmd.String("points")

	// Detect cache format to decide loading path
	isV2 := detectV2Cache(cacheFile)

	var rgeo geocoder.Geocoder
	var closer io.Closer

	if isV2 {
		log.Info("Detected v2 cache, loading via mmap")
		rgeoDisk, err := geocoder.LoadGeoCoderFromFileDisk(cacheFile,
			geocoder.WithLogger(log), geocoder.WithSearchRadius(radius))
		if err != nil {
			return err
		}
		rgeo = rgeoDisk
		closer = rgeoDisk
	} else {
		rgeoMem, err := geocoder.LoadGeoCoderFromFile(cacheFile,
			geocoder.WithLogger(log), geocoder.WithSearchRadius(radius))
		if err != nil {
			return err
		}
		rgeo = rgeoMem
	}

	if closer != nil {
		defer closer.Close()
	}

	var fgeo *fgeocode.Geocoder
	if cmd.Bool("fgeocode") {
		source, ok := rgeo.(fgeocode.Source)
		if !ok {
			log.Warn("geocoder does not support forward indexing")
		} else {
			fgeo = fgeocode.New(fgeocode.Config{
				Source:    source,
				IndexDir:  cmd.String("fgeocode-index"),
				CacheFile: cacheFile,
				Logger:    log,
			})
			defer fgeo.Close()
		}
	}

	runtime.GC()

	return server.Run(ctx, cmd.String("listen"), rgeo, fgeo, pointsPerThread, log)
}

// detectV2Cache reads the first 8 bytes of a cache file and returns true
// if it's a v2 format cache (magic "RGEO" + compat level 2).
func detectV2Cache(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()

	var magic [4]byte
	if _, err := f.Read(magic[:]); err != nil {
		return false
	}
	if string(magic[:]) != "RGEO" {
		return false
	}

	var compatBuf [4]byte
	if _, err := f.Read(compatBuf[:]); err != nil {
		return false
	}

	return binary.LittleEndian.Uint32(compatBuf[:]) == savev2.COMPATIBILITY_LEVEL
}

func tuneGC() error {
	_, err := memlimit.Set(
		memlimit.WithRatio(0.85),
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

	// Generation is memory-bound, not allocation-throughput-bound. Keep normal
	// collection pacing and a soft limit below the host RAM: with GOGC=off the
	// heap grew to tens of gigabytes and the process was OOM-killed on CI.
	debug.SetGCPercent(100)
	return nil
}
