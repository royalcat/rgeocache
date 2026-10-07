package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/royalcat/osmpbfdb"
	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/fgeocode"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geoparser"
	"github.com/thejerf/slogassert"
	"golang.org/x/exp/mmap"
)

func TestLondon(t *testing.T) {
	slogassert.NewDefault(t)
	var pointsFile = filepath.Join(t.TempDir(), "gb_points.rgc")

	t.Log("Downloading OSM file")

	const osmFileName = LondonFileName
	err := DownloadTestOSMFile(LondonFileURL, osmFileName)
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

	pointsFileOut, err := os.Create(pointsFile)
	if err != nil {
		t.Fatal(err)
	}

	gg, err := geoparser.NewGeoGen(osmdb, geoparser.ConfigDefault())
	if err != nil {
		t.Fatal(err)
	}

	err = gg.ParseOSMData([]geoparser.ParseOutput{{Format: "v2", Writer: pointsFileOut}})
	if err != nil {
		t.Fatal(err)
	}
	pointsFileOut.Close()

	t.Log("Checking road graph section")

	graphFile, err := os.Open(pointsFile)
	if err != nil {
		t.Fatal(err)
	}
	defer graphFile.Close()

	graphStat, err := graphFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	graphSection, err := savev2.FindGraphSection(graphFile, graphStat.Size())
	if err != nil {
		t.Fatalf("expected a graph section: %v", err)
	}
	if graphSection.EdgeCount == 0 {
		t.Fatal("expected graph edges")
	}
	if graphSection.MaxHalfExtent <= 0 {
		t.Fatalf("expected positive max half extent, got %v", graphSection.MaxHalfExtent)
	}
	for i := uint64(0); i < graphSection.EdgeCount && i < 16; i++ {
		rec, err := graphSection.Edge(graphFile, i)
		if err != nil {
			t.Fatalf("edge[%d]: %v", i, err)
		}
		if rec.FromPos == rec.ToPos {
			t.Errorf("edge[%d] is a self loop at position %d", i, rec.FromPos)
		}
		if rec.Class < 1 || rec.Class > 5 {
			t.Errorf("edge[%d] has invalid class %d", i, rec.Class)
		}
		if rec.Oneway > 2 {
			t.Errorf("edge[%d] has invalid oneway %d", i, rec.Oneway)
		}
	}
	t.Logf("Graph section: %d edges, max half extent %.6f", graphSection.EdgeCount, graphSection.MaxHalfExtent)

	t.Log("Loading points from file")

	rgeo, err := geocoder.LoadGeoCoderFromFileDisk(pointsFile, geocoder.WithSearchRadius(1))
	if err != nil {
		t.Fatal(err)
	}
	defer rgeo.Close()

	i, ok := rgeo.Find(51.501834, -0.125409)
	if !ok {
		t.Fatal("not found")
	}
	if i.City != "Greater London" || i.Street != "Cannon Row" || i.HouseNumber != "1" {
		t.Fatalf("expected Greater London, Cannon Row, 1; got %s, %s, %s", i.City, i.Street, i.HouseNumber)
	}

	t.Log("Forward geocoding")

	fgeo := fgeocode.New(fgeocode.Config{Source: rgeo})
	fgeo.Build(context.Background())
	if err := fgeo.Ready(); err != nil {
		t.Fatalf("forward geocoder build: %v", err)
	}
	defer fgeo.Close()

	results, err := fgeo.Search(fgeocode.SearchRequest{Query: "Cannon Row", Limit: 10})
	if err != nil {
		t.Fatalf("forward search: %v", err)
	}
	found := false
	for _, result := range results {
		if strings.Contains(result.AddressString, "Cannon Row") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a 'Cannon Row' result, got %+v", results)
	}
}
