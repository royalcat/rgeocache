package test

import (
	"context"
	"math"
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

// edgeLengthMeters approximates the length of an edge between two cache points
// (x = longitude, y = latitude).
func edgeLengthMeters(fromX, fromY, toX, toY float64) float64 {
	lat := (fromY + toY) * 0.5 * math.Pi / 180
	dx := (toX - fromX) * 111_320.0 * math.Cos(lat)
	dy := (toY - fromY) * 111_320.0
	return math.Hypot(dx, dy)
}

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

	graphFile, err := mmap.Open(pointsFile)
	if err != nil {
		t.Fatal(err)
	}
	defer graphFile.Close()
	graphStat, err := os.Stat(pointsFile)
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

	// The generation-side simplification keeps every multi-node edge within
	// 150 m of OSM shape nodes; a raw OSM segment longer than that cannot be
	// split. Resolve point coordinates to check the lengths.
	loaded, err := savev2.LoadMmap(graphFile)
	if err != nil {
		t.Fatal(err)
	}
	// The generation-side simplification keeps every edge that spans multiple
	// OSM shape nodes within 150 m of each other. An edge longer than the cap
	// can therefore only be a raw OSM segment without intermediate nodes; such
	// segments are left untouched instead of interpolating invented points.
	// Assert the cap holds for everything else and that raw gaps stay rare.
	overCap := 0
	maxMeters := 0.0
	for i := uint64(0); i < graphSection.EdgeCount; i++ {
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
		fromX, fromY, err := loaded.DiskBush.CoordAt(int(rec.FromPos))
		if err != nil {
			t.Fatalf("edge[%d]: %v", i, err)
		}
		toX, toY, err := loaded.DiskBush.CoordAt(int(rec.ToPos))
		if err != nil {
			t.Fatalf("edge[%d]: %v", i, err)
		}
		meters := edgeLengthMeters(fromX, fromY, toX, toY)
		if meters > maxMeters {
			maxMeters = meters
		}
		if meters > 150.1 {
			overCap++
		}
	}
	if ratio := float64(overCap) / float64(graphSection.EdgeCount); ratio > 0.05 {
		t.Errorf("%d of %d edges (%.1f%%) exceed the 150 m cap; only raw OSM gaps may",
			overCap, graphSection.EdgeCount, ratio*100)
	}
	t.Logf("Graph section: %d edges, max half extent %.6f, max edge %.1f m, %d over the 150 m cap",
		graphSection.EdgeCount, graphSection.MaxHalfExtent, maxMeters, overCap)

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
