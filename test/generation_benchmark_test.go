package test

import (
	"testing"

	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/geoparser"
	"golang.org/x/exp/mmap"
)

func BenchmarkGenerationLondon(b *testing.B) {
	b.Log("Downloading OSM file")

	err := downloadTestOSMFile(londonFileURL, londonFileName)
	if err != nil {
		b.Fatal(err)
	}

	b.Log("Parsing OSM file")

	b.ResetTimer()

	for b.Loop() {
		file, err := mmap.Open(londonFileName)
		if err != nil {
			b.Fatal(err)
		}

		osmdb, err := osmpbfdb.OpenDB(file, osmpbfdb.Config{})
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
	b.Log("Downloading OSM file")

	err := downloadTestOSMFile(greatBritanURL, greatBritanName)
	if err != nil {
		b.Fatal(err)
	}

	b.Log("Parsing OSM file")

	for b.Loop() {
		file, err := mmap.Open(greatBritanName)
		if err != nil {
			b.Fatal(err)
		}

		osmdb, err := osmpbfdb.OpenDB(file, osmpbfdb.Config{})
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
	}
}
