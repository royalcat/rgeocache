package server

import (
	"testing"

	"github.com/royalcat/rgeocache/geocoder"
	"github.com/valyala/fasthttp"
)

func BenchmarkHandlers(b *testing.B) {
	s := &server{
		rgeo: geocoder.NewRGeoCoder(),
	}

	b.ResetTimer()

	b.Run("RGeoMultipleCodeHandler-10", func(b *testing.B) {
		points := genereatePoints(10)
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			ctx := getRequestCtx(points)
			s.RGeoMultipleCodeHandler(ctx)
		}
	})

	b.Run("RGeoMultipleCodeHandler-1000", func(b *testing.B) {
		points := genereatePoints(1000)
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			ctx := getRequestCtx(points)
			s.RGeoMultipleCodeHandler(ctx)
		}
	})

	b.Run("RGeoMultipleCodeHandler-10_000", func(b *testing.B) {
		points := genereatePoints(10_000)
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			ctx := getRequestCtx(points)
			s.RGeoMultipleCodeHandler(ctx)
		}
	})
}

func genereatePoints(n int) string {
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
