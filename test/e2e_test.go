package test

import (
	"runtime"
	"testing"

	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geoparser"
	"golang.org/x/exp/mmap"
)

func TestLondon(t *testing.T) {
	const pointsFile = "gb_points.rgc"

	t.Log("Downloading OSM file")

	err := downloadTestOSMFile(greatBritanURL, greatBritanName)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Parsing OSM file")

	file, err := mmap.Open(greatBritanName)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	osmdb, err := osmpbfdb.OpenDB(file, osmpbfdb.Config{})
	if err != nil {
		t.Fatal(err)
	}

	gg, err := geoparser.NewGeoGen(osmdb, runtime.GOMAXPROCS(0), "")
	if err != nil {
		t.Fatal(err)
	}

	err = gg.ParseOSMData()
	if err != nil {
		t.Fatal(err)
	}

	err = gg.SavePointsToFile(pointsFile)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Loading points from file")

	rgeo, err := geocoder.LoadGeoCoderFromFile(pointsFile)
	if err != nil {
		t.Fatal(err)
	}

	i, ok := rgeo.Find(51.501834, -0.125409)
	if !ok {
		t.Fatal("not found")
	}
	if i.Region != "England" || i.City != "London" || i.Street != "Cannon Row" || i.HouseNumber != "1" {
		t.Fatalf("expected England, London, Cannon Row, 1; got %s, %s, %s, %s", i.Region, i.City, i.Street, i.HouseNumber)
	}
}
