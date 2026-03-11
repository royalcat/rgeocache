package server

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/royalcat/rgeocache/cachesaver"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/kdbush"
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

func BenchmarkSaveLoad(b *testing.B) {
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

	b.Log("Loading raw points and zones from file")

	rawPoints, rawZones, err := cachesaver.LoadFromReader(must(openTestFile(pointsFile)), slog.Default())
	if err != nil {
		b.Fatalf("Failed to load raw points: %v", err)
	}

	b.Logf("Loaded %d points and %d zones", len(rawPoints), len(rawZones))

	// Pre-serialize into a buffer for the load benchmark
	var serialized bytes.Buffer
	pointsSeq := kdbushPointsToSeq(rawPoints)
	zonesSeq := sliceToSeq(rawZones)
	meta := cachemodel.Metadata{
		Version:     1,
		Locale:      "en",
		DateCreated: time.Now(),
	}

	err = cachesaver.Save(pointsSeq, zonesSeq, meta, &serialized)
	if err != nil {
		b.Fatalf("Failed to pre-serialize data: %v", err)
	}

	serializedBytes := serialized.Bytes()

	b.Logf("Serialized size: %d bytes", len(serializedBytes))

	b.ResetTimer()

	b.Run("Save", func(b *testing.B) {
		for b.Loop() {
			err := cachesaver.Save(
				kdbushPointsToSeq(rawPoints),
				sliceToSeq(rawZones),
				meta,
				io.Discard,
			)
			if err != nil {
				b.Fatalf("Save failed: %v", err)
			}
		}
	})

	b.Run("Load", func(b *testing.B) {
		for b.Loop() {
			reader := bytes.NewReader(serializedBytes)
			points, zones, err := cachesaver.LoadFromReader(reader, slog.Default())
			if err != nil {
				b.Fatalf("Load failed: %v", err)
			}
			_ = points
			_ = zones
		}
	})
}

func openTestFile(path string) (io.ReadCloser, error) {
	return os.Open(path)
}

func kdbushPointsToSeq(points []kdbush.Point[cachemodel.Info]) func(yield func(cachemodel.Point) bool) {
	return func(yield func(cachemodel.Point) bool) {
		for _, p := range points {
			if !yield(p) {
				return
			}
		}
	}
}

func sliceToSeq[T any](slice []T) func(yield func(T) bool) {
	return func(yield func(T) bool) {
		for _, v := range slice {
			if !yield(v) {
				return
			}
		}
	}
}
