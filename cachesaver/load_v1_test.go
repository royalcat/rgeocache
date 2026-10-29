package cachesaver

import (
	"bytes"
	"encoding/binary"
	"log/slog"
	"slices"
	"testing"
	"time"
	"unique"

	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
)

// TestLoadV1CacheSkipsNamelessRoads verifies that the v1 loader drops road
// shape points that only exist to carry the v2 road graph: weight 5 with no
// street and no name. A city recorded on such a point does not save it, while
// buildings, named roads and named areas are kept.
func TestLoadV1CacheSkipsNamelessRoads(t *testing.T) {
	meta := cachemodel.Metadata{
		Version:     9,
		DateCreated: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		Locale:      "ru",
	}

	points := []cachemodel.Point{
		{X: 1, Y: 10, Data: testV1Info("Cafe", "Tverskaya", "12", "Moscow", "Moscow", 10)},
		{X: 2, Y: 20, Data: testV1Info("", "Tverskaya", "", "Moscow", "Moscow", 5)},
		// Nameless road with a settlement: still a graph shape point.
		{X: 3, Y: 30, Data: testV1Info("", "", "", "Moscow", "Moscow", 5)},
		// Nameless road in a region without a settlement.
		{X: 4, Y: 40, Data: testV1Info("", "", "", "", "Moscow", 5)},
		{X: 5, Y: 50, Data: testV1Info("Protected Area", "", "", "", "Moscow", 2)},
	}

	poly := orb.MultiPolygon{orb.Polygon{orb.Ring{
		{10, 20}, {11, 20}, {11, 21}, {10, 21}, {10, 20},
	}}}
	zones := []cachemodel.Zone{{
		Type: cachemodel.ZoneRegion, Name: unique.Make("Moscow"),
		Bounds: poly.Bound(), Polygon: poly,
	}}

	var payload bytes.Buffer
	if err := savev1.Save(&payload, slices.Values(points), slices.Values(zones), meta); err != nil {
		t.Fatalf("save v1: %v", err)
	}

	var cache bytes.Buffer
	cache.Write(MAGIC_BYTES)
	if err := binary.Write(&cache, binary.LittleEndian, savev1.COMPATIBILITY_LEVEL); err != nil {
		t.Fatalf("write compatibility level: %v", err)
	}
	cache.Write(payload.Bytes())

	loaded, loadedZones, loadedMeta, err := LoadFromReader(&cache, slog.Default())
	if err != nil {
		t.Fatalf("load v1: %v", err)
	}

	wantX := []float64{1, 2, 5}
	if len(loaded) != len(wantX) {
		t.Fatalf("point count: got %d, want %d", len(loaded), len(wantX))
	}
	for i, want := range wantX {
		if loaded[i].X != want {
			t.Errorf("point %d X: got %v, want %v", i, loaded[i].X, want)
		}
	}
	if len(loadedZones) != 1 || loadedZones[0].Name.Value() != "Moscow" {
		t.Fatalf("zones not preserved: %+v", loadedZones)
	}
	if loadedMeta == nil || loadedMeta.Version != meta.Version || !loadedMeta.DateCreated.Equal(meta.DateCreated) || loadedMeta.Locale != meta.Locale {
		t.Fatalf("metadata mismatch: got %+v, want %+v", loadedMeta, meta)
	}
}

func testV1Info(name, street, house, city, region string, weight uint8) cachemodel.Info {
	return cachemodel.Info{
		Name:        unique.Make(name),
		Street:      unique.Make(street),
		HouseNumber: unique.Make(house),
		City:        unique.Make(city),
		Region:      unique.Make(region),
		Weight:      weight,
	}
}
