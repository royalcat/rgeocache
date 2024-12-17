package test

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/paulmach/osm"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/royalcat/rgeocache/geoparser"
)

const (
	// Originally downloaded from http://download.geofabrik.de/europe/great-britain/england/greater-london.html
	londonFileName = "greater-london-140324.osm.pbf"
	londonFileURL  = "https://gist.githubusercontent.com/paulmach/853d57b83d408480d3b148b07954c110/raw/853f33f4dbe4246915134f1cde8edb30241ecc10/greater-london-140324.osm.pbf"

	// TODO replace with static file
	greatBritanName = "great-britain-latest.osm.pbf"
	greatBritanURL  = "https://download.geofabrik.de/europe/great-britain-latest.osm.pbf"

	coordinatesPrecision = 1e7
)

type OSMFileTest struct {
	FileName string
	FileURL  string
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func stripCoordinates(w *osm.Way) *osm.Way {
	if w == nil {
		return nil
	}

	ws := new(osm.Way)
	*ws = *w
	ws.Nodes = make(osm.WayNodes, len(w.Nodes))
	for i, n := range w.Nodes {
		n.Lat, n.Lon = 0, 0
		ws.Nodes[i] = n
	}
	return ws
}

func roundCoordinates(w *osm.Way) {
	if w == nil {
		return
	}
	for i := range w.Nodes {
		w.Nodes[i].Lat = math.Round(w.Nodes[i].Lat*coordinatesPrecision) / coordinatesPrecision
		w.Nodes[i].Lon = math.Round(w.Nodes[i].Lon*coordinatesPrecision) / coordinatesPrecision
	}
}

func downloadTestOSMFile(url, fileName string) error {
	if _, err := os.Stat(fileName); os.IsNotExist(err) {
		out, err := os.Create(fileName)
		if err != nil {
			return err
		}
		defer out.Close()

		resp, err := http.Get(url)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("test status code invalid: %v", resp.StatusCode)
		}

		if _, err := io.Copy(out, resp.Body); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	return nil
}

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

	i, ok := rgeo.Find(51.501900, -0.125313)
	if !ok {
		t.Fatal("not found")
	}
	if i.Region != "England" || i.City != "London" || i.Street != "Cannon Row" || i.HouseNumber != "1" {
		t.Fatalf("expected England, London, Cannon Row, 1; got %s, %s, %s, %s", i.Region, i.City, i.Street, i.HouseNumber)
	}
}

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
