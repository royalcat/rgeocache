package server

import (
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"unique"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
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

	var pointsPerCall = []int{10, 1000, 10_000, 50_000, 80_000}

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

// buildTestGeoCoder creates an RGeoCoder populated with n test points.
// Each point has a unique coordinate (i*0.01, i*0.01) and a name "point-{i}".
func buildTestGeoCoder(t *testing.T, n int) *geocoder.RGeoCoder {
	t.Helper()
	points := make([]cachemodel.Point, n)
	for i := range n {
		points[i] = cachemodel.Point{
			X: float64(i) * 0.01,
			Y: float64(i) * 0.01,
			Data: cachemodel.Info{
				Name:        unique.Make(fmt.Sprintf("point-%d", i)),
				Street:      unique.Make("Test Street"),
				HouseNumber: unique.Make(strconv.Itoa(i)),
				City:        unique.Make(""),
				Region:      unique.Make(""),
				Weight:      10,
			},
		}
	}
	return geocoder.NewGeoCoderFromPoints(points, geocoder.WithSearchRadius(0.1))
}

// makeInput creates [][2]float64 input from test points at indices [0, n).
// Each input coordinate matches a test geocoder point created by buildTestGeoCoder.
func makeInput(t *testing.T, n int) [][2]float64 {
	t.Helper()
	input := make([][2]float64, n)
	for i := range n {
		input[i] = [2]float64{float64(i) * 0.01, float64(i) * 0.01} // lat, lon
	}
	return input
}

func TestMultithreadedFind(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		rgeo := buildTestGeoCoder(t, 0)
		s := &server{rgeo: rgeo}
		result := s.multithreadedFind(nil, 4)
		if len(result) != 0 {
			t.Errorf("expected 0 results, got %d", len(result))
		}
	})

	t.Run("single point", func(t *testing.T) {
		rgeo := buildTestGeoCoder(t, 1)
		s := &server{rgeo: rgeo}
		input := makeInput(t, 1)
		result := s.multithreadedFind(input, 1)
		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}
		if result[0].Name != "point-0" {
			t.Errorf("expected point-0, got %q", result[0].Name)
		}
	})

	t.Run("order preserved with many threads", func(t *testing.T) {
		const numPoints = 5000
		const numThreads = 16

		rgeo := buildTestGeoCoder(t, numPoints)
		s := &server{rgeo: rgeo}
		input := makeInput(t, numPoints)

		result := s.multithreadedFind(input, numThreads)

		if len(result) != numPoints {
			t.Fatalf("expected %d results, got %d", numPoints, len(result))
		}

		for i, r := range result {
			expected := fmt.Sprintf("point-%d", i)
			if r.Name != expected {
				t.Errorf("index %d: expected name %q, got %q", i, expected, r.Name)
			}
		}
	})

	t.Run("order preserved single thread", func(t *testing.T) {
		const numPoints = 2000
		const numThreads = 1

		rgeo := buildTestGeoCoder(t, numPoints)
		s := &server{rgeo: rgeo}
		input := makeInput(t, numPoints)

		result := s.multithreadedFind(input, numThreads)

		if len(result) != numPoints {
			t.Fatalf("expected %d results, got %d", numPoints, len(result))
		}

		for i, r := range result {
			expected := fmt.Sprintf("point-%d", i)
			if r.Name != expected {
				t.Errorf("index %d: expected name %q, got %q", i, expected, r.Name)
			}
		}
	})

	t.Run("more threads than points", func(t *testing.T) {
		const numPoints = 100
		const numThreads = 200

		rgeo := buildTestGeoCoder(t, numPoints)
		s := &server{rgeo: rgeo}
		input := makeInput(t, numPoints)

		result := s.multithreadedFind(input, numThreads)

		if len(result) != numPoints {
			t.Fatalf("expected %d results, got %d", numPoints, len(result))
		}

		for i, r := range result {
			expected := fmt.Sprintf("point-%d", i)
			if r.Name != expected {
				t.Errorf("index %d: expected name %q, got %q", i, expected, r.Name)
			}
		}
	})

	t.Run("threshold boundary", func(t *testing.T) {
		// Exactly 1000 points — the threshold where multithreaded path is chosen
		const numPoints = 1000
		const numThreads = 4

		rgeo := buildTestGeoCoder(t, numPoints)
		s := &server{rgeo: rgeo}
		input := makeInput(t, numPoints)

		result := s.multithreadedFind(input, numThreads)

		if len(result) != numPoints {
			t.Fatalf("expected %d results, got %d", numPoints, len(result))
		}

		for i, r := range result {
			expected := fmt.Sprintf("point-%d", i)
			if r.Name != expected {
				t.Errorf("index %d: expected name %q, got %q", i, expected, r.Name)
			}
		}
	})

	t.Run("house number preserved", func(t *testing.T) {
		const numPoints = 100
		rgeo := buildTestGeoCoder(t, numPoints)
		s := &server{rgeo: rgeo}
		input := makeInput(t, numPoints)

		result := s.multithreadedFind(input, 4)

		for i, r := range result {
			expectedHN := strconv.Itoa(i)
			if r.HouseNumber != expectedHN {
				t.Errorf("index %d: expected HouseNumber %q, got %q", i, expectedHN, r.HouseNumber)
			}
		}
	})
}
