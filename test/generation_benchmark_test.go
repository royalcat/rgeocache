package test

import (
	"context"
	"runtime"
	"testing"

	"github.com/royalcat/rgeocache/geoparser"
)

func BenchmarkGenerationLondon(b *testing.B) {
	ctx := context.Background()

	b.Log("Downloading OSM file")

	err := downloadTestOSMFile(londonFileURL, londonFileName)
	if err != nil {
		b.Fatal(err)
	}

	b.Log("Parsing OSM file")

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		gg, err := geoparser.NewGeoGen(runtime.GOMAXPROCS(0), "")
		if err != nil {
			b.Fatal(err)
		}

		err = gg.ParseOSMFile(ctx, londonFileName)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerationGreatBritan(b *testing.B) {
	ctx := context.Background()

	b.Log("Downloading OSM file")

	err := downloadTestOSMFile(greatBritanURL, greatBritanName)
	if err != nil {
		b.Fatal(err)
	}

	b.Log("Parsing OSM file")

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		gg, err := geoparser.NewGeoGen(runtime.GOMAXPROCS(0), "")
		if err != nil {
			b.Fatal(err)
		}

		err = gg.ParseOSMFile(ctx, greatBritanName)
		if err != nil {
			b.Fatal(err)
		}
	}
}
