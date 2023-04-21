package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geoparser"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
	_ "go.uber.org/automaxprocs"
)

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

	geoGen, err := geoparser.NewGeoGen(cache, true, threads)
	if err != nil {
		return fmt.Errorf("error creating geoGen: %w", err)
	}
	defer geoGen.Close()

	inputs := ctx.StringSlice("input")
	fmt.Printf("Input maps: %v\n", inputs)
	for _, v := range inputs {
		fmt.Printf("Generating database for map: %s\n", v)
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
	err = geoGen.SavePointsToFile(ctx.String("points"))
	if err != nil {
		return fmt.Errorf("failed to save points to file: %s", err.Error())
	}

	return nil
}

func serve(ctx *cli.Context) error {
	srv := &Server{
		rgeo: &geocoder.RGeoCoder{},
	}
	log := logrus.New()
	log.Info("Initing geocoder")
	err := srv.rgeo.LoadFromPointsFile(ctx.String("points"))
	if err != nil {
		return err
	}
	return srv.ListenAndServe(ctx.Context, ctx.String("listen"))
}
