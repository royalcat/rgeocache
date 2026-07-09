package main

import (
	"context"
	"log"
	"os"

	"github.com/urfave/cli/v3"
)

func main() {
	app := &cli.Command{
		Name:        "simulate",
		Description: "Simulate load on a rgeocache server or compare two servers",
		Commands: []*cli.Command{
			{
				Name:        "bench",
				Usage:       "Benchmark a rgeocache server with random point queries",
				Description: "Sends requests with random coordinates to a single server and reports latency statistics.",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "server",
						Usage: "rgeocache server URL",
						Value: "http://localhost:8080",
					},
					&cli.IntFlag{
						Name:  "count",
						Usage: "number of random points per request",
						Value: 100,
					},
					&cli.IntFlag{
						Name:  "workers",
						Usage: "number of concurrent workers",
						Value: 1,
					},
					&cli.IntFlag{
						Name:  "repeat",
						Usage: "total number of requests to send",
						Value: 1,
					},
					&cli.Float64Flag{
						Name:  "min-lat",
						Usage: "minimum latitude of bounding box",
						Value: 51.3,
					},
					&cli.Float64Flag{
						Name:  "max-lat",
						Usage: "maximum latitude of bounding box",
						Value: 51.7,
					},
					&cli.Float64Flag{
						Name:  "min-lon",
						Usage: "minimum longitude of bounding box",
						Value: -0.5,
					},
					&cli.Float64Flag{
						Name:  "max-lon",
						Usage: "maximum longitude of bounding box",
						Value: 0.2,
					},
					&cli.DurationFlag{
						Name:  "timeout",
						Usage: "HTTP request timeout",
						Value: 0, // default handled in action
					},
				},
				Action: bench,
			},
			{
				Name:        "compare",
				Usage:       "Compare two rgeocache servers and report discrepancies",
				Description: "Sends identical random-coordinate requests to two servers and compares their responses field by field.",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:  "server-a",
						Usage: "rgeocache server A URL",
						Value: "http://localhost:8080",
					},
					&cli.StringFlag{
						Name:     "server-b",
						Usage:    "rgeocache server B URL",
						Required: true,
					},
					&cli.IntFlag{
						Name:  "count",
						Usage: "number of random points per request",
						Value: 100,
					},
					&cli.IntFlag{
						Name:  "workers",
						Usage: "number of concurrent workers",
						Value: 1,
					},
					&cli.IntFlag{
						Name:  "repeat",
						Usage: "total number of requests to send",
						Value: 1,
					},
					&cli.Float64Flag{
						Name:  "min-lat",
						Usage: "minimum latitude of bounding box",
						Value: 51.3,
					},
					&cli.Float64Flag{
						Name:  "max-lat",
						Usage: "maximum latitude of bounding box",
						Value: 51.7,
					},
					&cli.Float64Flag{
						Name:  "min-lon",
						Usage: "minimum longitude of bounding box",
						Value: -0.5,
					},
					&cli.Float64Flag{
						Name:  "max-lon",
						Usage: "maximum longitude of bounding box",
						Value: 0.2,
					},
					&cli.DurationFlag{
						Name:  "timeout",
						Usage: "HTTP request timeout",
						Value: 0, // default handled in action
					},
				},
				Action: compare,
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
