package test

import (
	"testing"

	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geoparser"
	"golang.org/x/exp/mmap"
)

func TestLondon(t *testing.T) {
	const pointsFile = "gb_points.rgc"

	t.Log("Downloading OSM file")

	const osmFileName = londonFileName
	err := downloadTestOSMFile(londonFileURL, osmFileName)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Parsing OSM file")

	file, err := mmap.Open(osmFileName)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	osmIndexDir := t.TempDir()
	t.Logf("OSM index directory: %s", osmIndexDir)
	osmdb, err := osmpbfdb.OpenDB(file, osmpbfdb.Config{
		IndexDir: osmIndexDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("OsmDB counts: nodes: %d ways: %d relations: %d", osmdb.CountNodes(), osmdb.CountWays(), osmdb.CountRelations())

	gg, err := geoparser.NewGeoGen(osmdb, geoparser.ConfigDefault())
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

	rgeo, err := geocoder.LoadGeoCoderFromFile(pointsFile, geocoder.WithSearchRadius(1))
	if err != nil {
		t.Fatal(err)
	}

	i, ok := rgeo.Find(51.501834, -0.125409)
	if !ok {
		t.Fatal("not found")
	}
	if i.City != "Greater London" || i.Street != "Cannon Row" || i.HouseNumber != "1" {
		t.Fatalf("expected Greater London, Cannon Row, 1; got %s, %s, %s", i.City, i.Street, i.HouseNumber)
	}
}
