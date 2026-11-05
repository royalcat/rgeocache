package cachesaver

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"slices"
	"testing"
	"time"
	"unique"

	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/kdbush"
)

func TestConvertV2ToV1(t *testing.T) {
	meta := cachemodel.Metadata{Version: 7, Locale: "en", DateCreated: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)}
	points := []cachemodel.Point{
		{X: 10.5, Y: 20.25, Data: cachemodel.Info{
			Name:        unique.Make("Cafe"),
			Street:      unique.Make("High Street"),
			HouseNumber: unique.Make("12"),
			City:        unique.Make("Town"),
			Region:      unique.Make("Region"),
			Weight:      10,
			Type:        cachemodel.GeoObjectBuilding,
		}},
		{X: 10.6, Y: 20.3, Data: cachemodel.Info{
			Name:        unique.Make(""),
			Street:      unique.Make("High Street"),
			HouseNumber: unique.Make(""),
			City:        unique.Make("Town"),
			Region:      unique.Make("Region"),
			Weight:      5,
			Type:        cachemodel.GeoObjectRoad,
		}},
		// A nameless graph-only road point: the v1 loader would drop it, and the
		// converter must not write it in the first place.
		{X: 10.7, Y: 20.35, Data: cachemodel.Info{
			Name:        unique.Make(""),
			Street:      unique.Make(""),
			HouseNumber: unique.Make(""),
			City:        unique.Make("Town"),
			Region:      unique.Make("Region"),
			Weight:      5,
			Type:        cachemodel.GeoObjectRoad,
		}},
	}
	poly := orb.MultiPolygon{orb.Polygon{orb.Ring{
		{10, 20}, {11, 20}, {11, 21}, {10, 21}, {10, 20},
	}}}
	zones := []cachemodel.Zone{
		{Type: cachemodel.ZoneRegion, Name: unique.Make("Region"), Bounds: poly.Bound(), Polygon: poly},
		{Type: cachemodel.ZoneCountry, Name: unique.Make("Country"), Bounds: poly.Bound(), Polygon: poly},
	}

	var v2 bytes.Buffer
	items := make([]cachemodel.Item, 0, len(points))
	for _, p := range points {
		items = append(items, cachemodel.Item{Kind: cachemodel.ItemPoint, Point: p})
	}
	if err := SaveV2(slices.Values(items), slices.Values(zones), meta, &v2); err != nil {
		t.Fatal(err)
	}

	path := t.TempDir() + "/cache.rgc"
	if err := os.WriteFile(path, v2.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var v1 bytes.Buffer
	if err := ConvertV2ToV1(path, &v1); err != nil {
		t.Fatal(err)
	}

	// The graph-less fallback keeps address-bearing roads and drops the
	// nameless one, so the raw file already has only two points.
	raw := rawV1Points(t, v1.Bytes()[8:])
	if len(raw) != 2 {
		t.Fatalf("raw v1 point count: got %d, want 2", len(raw))
	}

	loadedPoints, loadedZones, loadedMeta, err := LoadFromReader(bytes.NewReader(v1.Bytes()), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if loadedMeta == nil || !loadedMeta.DateCreated.Equal(meta.DateCreated) || loadedMeta.Version != meta.Version || loadedMeta.Locale != meta.Locale {
		t.Fatalf("metadata mismatch: %+v", loadedMeta)
	}
	if len(loadedPoints) != 2 {
		t.Fatalf("point count: got %d, want 2", len(loadedPoints))
	}
	for i, want := range points[:2] {
		got := loadedPoints[i]
		if got.X != want.X || got.Y != want.Y || got.Data.Name != want.Data.Name ||
			got.Data.Street != want.Data.Street || got.Data.HouseNumber != want.Data.HouseNumber ||
			got.Data.City != want.Data.City || got.Data.Region != want.Data.Region ||
			got.Data.Weight != want.Data.Weight {
			t.Fatalf("point %d mismatch: got %+v, want %+v", i, got, want)
		}
	}
	if len(loadedZones) != len(zones) {
		t.Fatalf("zone count: got %d, want %d", len(loadedZones), len(zones))
	}
	for i := range zones {
		if loadedZones[i].Type != zones[i].Type || loadedZones[i].Name != zones[i].Name {
			t.Fatalf("zone %d mismatch: got %+v, want %+v", i, loadedZones[i], zones[i])
		}
		if math.Abs(loadedZones[i].Bounds.Min.X()-zones[i].Bounds.Min.X()) > 1e-5 {
			t.Fatalf("zone %d bounds mismatch: got %+v, want %+v", i, loadedZones[i].Bounds, zones[i].Bounds)
		}
	}
}

// TestConvertV2ToV1RoadModes builds a cache with a building, a major road, a
// residential road and a nameless major road, then verifies which points each
// road mode writes to v1.
func TestConvertV2ToV1RoadModes(t *testing.T) {
	const (
		majorStreet = "Major Street"
		minorStreet = "Minor Street"
		city        = "Town"
		region      = "Region"
	)

	building := cachemodel.Item{Kind: cachemodel.ItemPoint, Point: cachemodel.Point{X: 9.9, Y: 20.0, Data: cachemodel.Info{
		Name:        unique.Make("Cafe"),
		Street:      unique.Make("Home Street"),
		HouseNumber: unique.Make("1"),
		City:        unique.Make(city),
		Region:      unique.Make(region),
		Weight:      10,
		Type:        cachemodel.GeoObjectBuilding,
	}}}

	roadPoint := func(x, y float64, street string, node uint32) cachemodel.Item {
		return cachemodel.Item{Kind: cachemodel.ItemPoint, GraphNode: node, Point: cachemodel.Point{X: x, Y: y, Data: cachemodel.Info{
			Name:        unique.Make(""),
			Street:      unique.Make(street),
			HouseNumber: unique.Make(""),
			City:        unique.Make(city),
			Region:      unique.Make(region),
			Weight:      5,
			Type:        cachemodel.GeoObjectRoad,
		}}}
	}
	edge := func(from, to uint32, street string, class uint8) cachemodel.Item {
		return cachemodel.Item{Kind: cachemodel.ItemEdge, Edge: cachemodel.GraphEdge{
			FromNode: from,
			ToNode:   to,
			Street:   unique.Make(street),
			Name:     unique.Make(""),
			Class:    class,
		}}
	}

	items := []cachemodel.Item{
		building,
		// Major road, two points ~220 m apart: both survive 150 m thinning.
		roadPoint(10.0, 20.010, majorStreet, 1),
		roadPoint(10.0, 20.012, majorStreet, 2),
		// Residential road, also two points in different cells.
		roadPoint(10.01, 20.020, minorStreet, 3),
		roadPoint(10.01, 20.022, minorStreet, 4),
		// Nameless major road.
		roadPoint(10.02, 20.030, "", 5),
		roadPoint(10.02, 20.032, "", 6),
		edge(1, 2, majorStreet, cachemodel.GraphClassPrimary),
		edge(3, 4, minorStreet, cachemodel.GraphClassResidential),
		edge(5, 6, "", cachemodel.GraphClassPrimary),
	}

	meta := cachemodel.Metadata{Version: 3, Locale: "en", DateCreated: time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)}
	var v2 bytes.Buffer
	if err := SaveV2(slices.Values(items), slices.Values([]cachemodel.Zone{}), meta, &v2); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/cache.rgc"
	if err := os.WriteFile(path, v2.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	convert := func(t *testing.T, opts ConvertOptions) []cachemodel.Point {
		t.Helper()
		var out bytes.Buffer
		if err := ConvertV2ToV1WithOptions(path, &out, opts); err != nil {
			t.Fatal(err)
		}
		return rawV1Points(t, out.Bytes()[8:])
	}

	counts := func(points []cachemodel.Point) (c roadCounts) {
		for _, p := range points {
			switch {
			case p.Data.Weight == 10:
				c.buildings++
			case p.Data.Weight == 5 && p.Data.Street.Value() == majorStreet:
				c.major++
			case p.Data.Weight == 5 && p.Data.Street.Value() == minorStreet:
				c.minor++
			case p.Data.Weight == 5 && p.Data.Street.Value() == "" && p.Data.Name.Value() == "":
				c.nameless++
			}
		}
		return c
	}

	t.Run("major thinned", func(t *testing.T) {
		got := counts(convert(t, ConvertOptions{Roads: V1RoadsMajor, RoadSpacingMeters: 150}))
		if got != (roadCounts{1, 2, 0, 0}) {
			t.Fatalf("counts: got %+v, want building 1, major 2, minor 0, nameless 0", got)
		}
	})

	t.Run("all thinned", func(t *testing.T) {
		got := counts(convert(t, ConvertOptions{Roads: V1RoadsAll, RoadSpacingMeters: 150}))
		if got != (roadCounts{1, 2, 2, 0}) {
			t.Fatalf("counts: got %+v, want building 1, major 2, minor 2, nameless 0", got)
		}
	})

	t.Run("none", func(t *testing.T) {
		got := counts(convert(t, ConvertOptions{Roads: V1RoadsNone}))
		if got != (roadCounts{1, 0, 0, 0}) {
			t.Fatalf("counts: got %+v, want only the building", got)
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		opts := ConvertOptions{Roads: V1RoadsMajor, RoadSpacingMeters: 150}
		var first, second bytes.Buffer
		if err := ConvertV2ToV1WithOptions(path, &first, opts); err != nil {
			t.Fatal(err)
		}
		if err := ConvertV2ToV1WithOptions(path, &second, opts); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Fatal("two conversions of the same cache differ")
		}
	})
}

// roadCounts is the tuple returned by the road-mode test counter.
type roadCounts struct {
	buildings, major, minor, nameless int
}

// TestV1PointFilterThinsByStreetAndCell checks the 150 m thinning rule
// directly: one point per street and grid cell, with different streets kept
// independently.
func TestV1PointFilterThinsByStreetAndCell(t *testing.T) {
	road := func(x, y float64, street uint32) kdbush.Point[savev2.V2PointData] {
		return kdbush.Point[savev2.V2PointData]{
			X: x,
			Y: y,
			Data: savev2.V2PointData{
				Weight:   5,
				StreetID: street,
			},
		}
	}

	f := &v1PointFilter{roads: V1RoadsAll, spacing: 150, seen: make(map[v1RoadCell]struct{})}

	if !f.keep(0, road(10.0, 20.0000, 1)) {
		t.Fatal("first point of the street was dropped")
	}
	if f.keep(1, road(10.0, 20.0002, 1)) {
		t.Fatal("second point of the same street and cell was kept")
	}
	if !f.keep(2, road(10.0, 20.0002, 2)) {
		t.Fatal("a different street in the same cell was dropped")
	}
	if !f.keep(3, road(10.0, 20.0020, 1)) {
		t.Fatal("the same street in another cell was dropped")
	}

	// Nameless road points are always dropped.
	if (&v1PointFilter{roads: V1RoadsAll}).keep(0, kdbush.Point[savev2.V2PointData]{X: 1, Y: 1, Data: savev2.V2PointData{Weight: 5}}) {
		t.Fatal("nameless road point was kept")
	}
	// Non-road points are always kept.
	if !(&v1PointFilter{roads: V1RoadsNone}).keep(0, kdbush.Point[savev2.V2PointData]{X: 1, Y: 1, Data: savev2.V2PointData{Weight: 10}}) {
		t.Fatal("building was dropped")
	}
}

// rawV1Points decodes the points of a v1 body (the file after the magic bytes
// and compatibility level), bypassing the loader's own road filter.
func rawV1Points(t *testing.T, body []byte) []cachemodel.Point {
	t.Helper()
	pointsIter, _, _, err := savev1.Load(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("load raw v1: %v", err)
	}
	var out []cachemodel.Point
	for p, err := range pointsIter {
		if err != nil {
			t.Fatalf("load raw v1 point: %v", err)
		}
		out = append(out, p)
	}
	return out
}
