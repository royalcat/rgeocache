package test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/geoparser"
	"github.com/thejerf/slogassert"
	"golang.org/x/exp/mmap"
)

func BenchmarkGenerationLondon(b *testing.B) {
	slogassert.NewDefault(b, slogassert.WithLeveler(slog.LevelDebug))
	var londonFileName = filepath.Join(b.TempDir(), LondonFileName)

	b.Log("Downloading OSM file")

	err := DownloadTestOSMFile(LondonFileURL, londonFileName)
	if err != nil {
		b.Fatal(err)
	}

	b.Log("Parsing OSM file")

	for b.Loop() {
		file, err := mmap.Open(londonFileName)
		if err != nil {
			b.Fatal(err)
		}

		osmdb, err := osmpbfdb.OpenDB(file, osmpbfdb.Config{
			IndexDir:  b.TempDir(),
			CacheType: osmpbfdb.CacheTypeWeak,
			SkipInfo:  true,
		})
		if err != nil {
			b.Fatal(err)
		}

		gg, err := geoparser.NewGeoGen(osmdb, geoparser.ConfigDefault())
		if err != nil {
			b.Fatal(err)
		}

		err = gg.ParseOSMData()
		if err != nil {
			b.Fatal(err)
		}

		osmdb.Close()
		file.Close()
	}
}

func BenchmarkGenerationGreatBritan(b *testing.B) {
	// Skip this benchmark for now
	b.SkipNow()

	slogassert.NewDefault(b, slogassert.WithLeveler(slog.LevelDebug))
	var greatBritanName = filepath.Join(b.TempDir(), GreatBritanOsmName)

	b.Log("Downloading OSM file")

	err := DownloadTestOSMFile(GreatBritanOsmURL, greatBritanName)
	if err != nil {
		b.Fatal(err)
	}

	b.Log("Parsing OSM file")

	for b.Loop() {
		file, err := mmap.Open(greatBritanName)
		if err != nil {
			b.Fatal(err)
		}

		osmdb, err := osmpbfdb.OpenDB(file, osmpbfdb.Config{
			IndexDir:  b.TempDir(),
			CacheType: osmpbfdb.CacheTypeWeak,
			SkipInfo:  true,
		})
		if err != nil {
			b.Fatal(err)
		}

		gg, err := geoparser.NewGeoGen(osmdb, geoparser.ConfigDefault())
		if err != nil {
			b.Fatal(err)
		}

		err = gg.ParseOSMData()
		if err != nil {
			b.Fatal(err)
		}

		file.Close()
		osmdb.Close()
	}
}
