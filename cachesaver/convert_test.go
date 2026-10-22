package cachesaver

import (
	"bytes"
	"math"
	"os"
	"slices"
	"testing"
	"time"
	"unique"

	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"log/slog"
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
	}
	poly := orb.MultiPolygon{orb.Polygon{orb.Ring{
		{10, 20}, {11, 20}, {11, 21}, {10, 21}, {10, 20},
	}}}
	zones := []cachemodel.Zone{
		{Type: cachemodel.ZoneRegion, Name: unique.Make("Region"), Bounds: poly.Bound(), Polygon: poly},
		{Type: cachemodel.ZoneCountry, Name: unique.Make("Country"), Bounds: poly.Bound(), Polygon: poly},
	}

	var v2 bytes.Buffer
	if err := SaveV2(slices.Values([]cachemodel.Item{
		{Kind: cachemodel.ItemPoint, Point: points[0]},
		{Kind: cachemodel.ItemPoint, Point: points[1]},
	}), slices.Values(zones), meta, &v2); err != nil {
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

	loadedPoints, loadedZones, loadedMeta, err := LoadFromReader(bytes.NewReader(v1.Bytes()), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if loadedMeta == nil || !loadedMeta.DateCreated.Equal(meta.DateCreated) || loadedMeta.Version != meta.Version || loadedMeta.Locale != meta.Locale {
		t.Fatalf("metadata mismatch: %+v", loadedMeta)
	}
	if len(loadedPoints) != len(points) {
		t.Fatalf("point count: got %d, want %d", len(loadedPoints), len(points))
	}
	for i := range points {
		got, want := loadedPoints[i], points[i]
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
