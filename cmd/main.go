package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path"
	"runtime"
	"runtime/pprof"
	"strings"

	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geoparser"
	"github.com/royalcat/rgeocache/server"

	_ "net/http/pprof"

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
		log.Fatal(err)
	}

}

func generate(ctx *cli.Context) error {
	log := slog.Default()

	threads := ctx.Int("threads")
	if threads == 0 {
		threads = runtime.GOMAXPROCS(0)
	}
	log = log.With("threads", threads)

	preferredLocalization := ctx.String("preferred-localization")
	if preferredLocalization == "official" {
		preferredLocalization = ""
	}

	if pprofListen := ctx.String("pprof.listen"); pprofListen != "" {
		go func() {
			log.Info("Starting pprof server")
			err := http.ListenAndServe(pprofListen, nil)
			if err != nil {
				log.Error("Error starting pprof server", "error", err)
			}
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
	if !strings.HasSuffix(saveFile, ".rgc") {
		saveFile = saveFile + ".rgc"
	}

	fmt.Printf("Generation complete\n")
	fmt.Printf("Saving to file: %s\n", saveFile)
	err = geoGen.SavePointsToFile(saveFile)
	if err != nil {
		return fmt.Errorf("failed to save points to file: %s", err.Error())
	}

	fmt.Printf("Complete\n")

	return nil
}

func writeHeapProfile(name string) error {
	f, err := os.Create(name + ".heap.prof")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	return pprof.WriteHeapProfile(f)
}

func serve(ctx *cli.Context) error {
	slog.Info("Initing geocoder")
	rgeo, err := geocoder.LoadGeoCoderFromFile(ctx.String("points"))
	if err != nil {
		return err
	}

	return server.Run(ctx.Context, ctx.String("listen"), rgeo)
}
