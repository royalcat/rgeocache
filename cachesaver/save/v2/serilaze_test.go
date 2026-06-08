package savev2

import (
	"bytes"
	"testing"
	"time"
	"unique"

	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

func makeTestMetadata() cachemodel.Metadata {
	return cachemodel.Metadata{
		Version:     2,
		Locale:      "en",
		DateCreated: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	points := []cachemodel.Point{
		{X: 51.5074, Y: -0.1278, Data: cachemodel.Info{
			Name:        unique.Make("Big Ben"),
			Street:      unique.Make("Bridge Street"),
			HouseNumber: unique.Make("1"),
			City:        unique.Make("London"),
			Region:      unique.Make("Greater London"),
			Weight:      10,
		}},
		{X: 48.8566, Y: 2.3522, Data: cachemodel.Info{
			Name:        unique.Make("Eiffel Tower"),
			Street:      unique.Make("Champ de Mars"),
			HouseNumber: unique.Make("5"),
			City:        unique.Make("Paris"),
			Region:      unique.Make("Île-de-France"),
			Weight:      10,
		}},
		{X: 40.6892, Y: -74.0445, Data: cachemodel.Info{
			Name:        unique.Make("Statue of Liberty"),
			Street:      unique.Make("Liberty Island"),
			HouseNumber: unique.Make(""),
			City:        unique.Make("New York"),
			Region:      unique.Make("New York"),
			Weight:      10,
		}},
	}

	// Zones: regions first, then countries (serialization order)
	zones := []cachemodel.Zone{
		{
			Type:   cachemodel.ZoneRegion,
			Name:   unique.Make("Greater London"),
			Bounds: orb.Bound{Min: orb.Point{-0.5, 51.3}, Max: orb.Point{0.3, 51.7}},
		},
		{
			Type:   cachemodel.ZoneCountry,
			Name:   unique.Make("United Kingdom"),
			Bounds: orb.Bound{Min: orb.Point{-8, 49}, Max: orb.Point{2, 59}},
		},
		{
			Type:   cachemodel.ZoneCountry,
			Name:   unique.Make("France"),
			Bounds: orb.Bound{Min: orb.Point{-5, 42}, Max: orb.Point{8, 51}},
		},
	}

	meta := makeTestMetadata()

	// Save to buffer
	var buf bytes.Buffer
	err := Save(&buf, sliceToSeq(points), sliceToSeq(zones), meta)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	t.Logf("Serialized size: %d bytes for %d points, %d zones", buf.Len(), len(points), len(zones))

	// Load from buffer
	loadedPoints := make([]cachemodel.Point, 0)
	loadedZones := make([]cachemodel.Zone, 0)

	pointsIter, zonesIter, loadedMeta, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	for p, err := range pointsIter {
		if err != nil {
			t.Fatalf("point error: %v", err)
		}
		loadedPoints = append(loadedPoints, p)
	}

	for z, err := range zonesIter {
		if err != nil {
			t.Fatalf("zone error: %v", err)
		}
		loadedZones = append(loadedZones, z)
	}

	// Verify metadata
	if loadedMeta.Version != meta.Version {
		t.Errorf("Version mismatch: %d != %d", loadedMeta.Version, meta.Version)
	}
	if loadedMeta.Locale != meta.Locale {
		t.Errorf("Locale mismatch: %s != %s", loadedMeta.Locale, meta.Locale)
	}

	// Verify counts
	if len(loadedPoints) != len(points) {
		t.Fatalf("Points count mismatch: %d != %d", len(loadedPoints), len(points))
	}
	if len(loadedZones) != len(zones) {
		t.Fatalf("Zones count mismatch: %d != %d", len(loadedZones), len(zones))
	}

	// Verify point data
	for i, p := range points {
		lp := loadedPoints[i]
		if lp.Data.Name.Value() != p.Data.Name.Value() {
			t.Errorf("Point[%d] Name mismatch: %q != %q", i, lp.Data.Name.Value(), p.Data.Name.Value())
		}
		if lp.Data.Street.Value() != p.Data.Street.Value() {
			t.Errorf("Point[%d] Street mismatch: %q != %q", i, lp.Data.Street.Value(), p.Data.Street.Value())
		}
		if lp.Data.City.Value() != p.Data.City.Value() {
			t.Errorf("Point[%d] City mismatch: %q != %q", i, lp.Data.City.Value(), p.Data.City.Value())
		}
		if lp.Data.Region.Value() != p.Data.Region.Value() {
			t.Errorf("Point[%d] Region mismatch: %q != %q", i, lp.Data.Region.Value(), p.Data.Region.Value())
		}
		if lp.Data.Weight != p.Data.Weight {
			t.Errorf("Point[%d] Weight mismatch: %d != %d", i, lp.Data.Weight, p.Data.Weight)
		}
	}

	// Verify zone names
	for i, z := range zones {
		lz := loadedZones[i]
		if lz.Name.Value() != z.Name.Value() {
			t.Errorf("Zone[%d] Name mismatch: %q != %q", i, lz.Name.Value(), z.Name.Value())
		}
		if lz.Type != z.Type {
			t.Errorf("Zone[%d] Type mismatch: %d != %d", i, lz.Type, z.Type)
		}
	}
}

func TestEmptySaveLoad(t *testing.T) {
	meta := makeTestMetadata()
	var buf bytes.Buffer

	err := Save(&buf, sliceToSeq([]cachemodel.Point{}), sliceToSeq([]cachemodel.Zone{}), meta)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	pointsIter, zonesIter, loadedMeta, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	count := 0
	for _, err := range pointsIter {
		if err != nil {
			t.Fatalf("point error: %v", err)
		}
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 points, got %d", count)
	}

	zoneCount := 0
	for _, err := range zonesIter {
		if err != nil {
			t.Fatalf("zone error: %v", err)
		}
		zoneCount++
	}
	if zoneCount != 0 {
		t.Errorf("expected 0 zones, got %d", zoneCount)
	}

	if loadedMeta.Version != 2 {
		t.Errorf("Version mismatch: %d != 2", loadedMeta.Version)
	}
}

func sliceToSeq[T any](slice []T) func(yield func(T) bool) {
	return func(yield func(T) bool) {
		for _, v := range slice {
			if !yield(v) {
				return
			}
		}
	}
}
