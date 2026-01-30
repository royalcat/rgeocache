package server

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/test"
	"github.com/thejerf/slogassert"
	"github.com/valyala/fasthttp"
)

func must[T any](val T, err error) T {
	if err != nil {
		panic(err)
	}
	return val
}

func BenchmarkHandlers(b *testing.B) {
	slogassert.NewDefault(b)
	pointsFile := filepath.Join(b.TempDir(), "gb_points.rgc")
	osmPbfName := filepath.Join(b.TempDir(), "db.osm.pbf")

	b.Log("Downloading OSM file")

	err := test.DownloadTestOSMFile(test.LondonFileURL, osmPbfName)
	if err != nil {
		b.Fatalf("Failed to download test OSM file: %v", err)
	}

	b.Log("Generating points")

	err = test.GeneratePoints(osmPbfName, pointsFile, b.TempDir())
	if err != nil {
		b.Fatalf("Failed to generate points: %v", err)
	}

	b.Log("Loading geocoder")

	rgeo, err := geocoder.LoadGeoCoderFromFile(pointsFile)
	if err != nil {
		b.Fatalf("Failed to load geocoder: %v", err)
	}

	s := &server{
		rgeo:                            rgeo,
		metricHttpAddressCallCount:      must(meter.Int64Counter("http_address_call_total")),
		metricHttpAddressMultiCallCount: must(meter.Int64Counter("http_address_multi_call_total")),
		metricAddressesEncoded:          must(meter.Int64Counter("address_encoded_total")),
	}

	b.Log("Warming up")

	// Warm up the server by making a few requests
	for range 10 {
		ctx := getRequestCtx(generatePoints(100))
		s.RGeoMultipleCodeHandler(ctx)
	}

	b.Log("Staring benchmark")

	b.ResetTimer()

	var pointsPerCall = []int{10, 1000, 10_000}

	for _, pointCount := range pointsPerCall {
		var pointTotal = 0
		b.Run(fmt.Sprintf("RGeoMultipleCodeHandler_%d", pointCount), func(b *testing.B) {
			points := generatePoints(pointCount)
			for b.Loop() {
				ctx := getRequestCtx(points)
				s.RGeoMultipleCodeHandler(ctx)
				pointTotal += len(points)
			}
			b.ReportMetric(float64(pointTotal)/float64(b.Elapsed().Seconds()), "points/s")
		})
	}
}

func generatePoints(n int) string {
	points := "["
	for i := range n {
		points += "[1.0, 1.0]"
		if i != n-1 {
			points += ","
		}
	}
	points += "]"
	return points
}

func getRequestCtx(body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	if body != "" {
		ctx.Request.SetBodyString(body)
	}
	return ctx
}
