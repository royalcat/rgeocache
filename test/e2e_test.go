package test

import (
	"context"
	"runtime"
	"testing"

	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geoparser"
)

func TestLondon(t *testing.T) {
	ctx := context.Background()

	t.Log("Downloading OSM file")

	err := downloadTestOSMFile(greatBritanURL, greatBritanName)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Parsing OSM file")

	gg, err := geoparser.NewGeoGen(runtime.GOMAXPROCS(0), "")
	if err != nil {
		t.Fatal(err)
	}

	const pointsFile = "gb_points.gob"

	err = gg.ParseOSMFile(ctx, greatBritanName)
	if err != nil {
		t.Fatal(err)
	}

	err = gg.SavePointsToFile("gb_points.gob")
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Loading points from file")

	rgeo := &geocoder.RGeoCoder{}
	err = rgeo.LoadFromPointsFile(pointsFile)
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
