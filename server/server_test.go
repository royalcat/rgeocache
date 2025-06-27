package server

import (
	"testing"

	"github.com/royalcat/rgeocache/geocoder"
	"github.com/valyala/fasthttp"
)

func must[T any](val T, err error) T {
	if err != nil {
		panic(err)
	}
	return val
}

func BenchmarkHandlers(b *testing.B) {
	rgeo, err := geocoder.LoadGeoCoderFromFile("../test/gb_points.rgc")
	if err != nil {
		b.Fatalf("Failed to load geocoder: %v", err)
	}

	s := &server{
		rgeo:                            rgeo,
		metricHttpAddressCallCount:      must(meter.Int64Counter("http_address_call_total")),
		metricHttpAddressMultiCallCount: must(meter.Int64Counter("http_address_multi_call_total")),
		metricAddressesEncoded:          must(meter.Int64Counter("address_encoded_total")),
	}

	b.ResetTimer()

	b.Run("RGeoMultipleCodeHandler-10", func(b *testing.B) {
		points := generatePoints(10)
		b.ResetTimer()

		for b.Loop() {
			ctx := getRequestCtx(points)
			s.RGeoMultipleCodeHandler(ctx)
		}
	})

	b.Run("RGeoMultipleCodeHandler-1000", func(b *testing.B) {
		points := generatePoints(1000)
		b.ResetTimer()

		for b.Loop() {
			ctx := getRequestCtx(points)
			s.RGeoMultipleCodeHandler(ctx)
		}
	})

	b.Run("RGeoMultipleCodeHandler-10_000", func(b *testing.B) {
		points := generatePoints(10_000)
		b.ResetTimer()

		for b.Loop() {
			ctx := getRequestCtx(points)
			s.RGeoMultipleCodeHandler(ctx)
		}
	})
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
