package geocoder

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
	"unique"

	"github.com/paulmach/orb"
	"github.com/royalcat/rgeocache/cachesaver"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

func pointsTestPoint(lon, lat float64, weight uint8, street, house, city string, typ cachemodel.GeoObjectType) cachemodel.Point {
	return cachemodel.Point{
		X: lon,
		Y: lat,
		Data: cachemodel.Info{
			Name:        unique.Make(""),
			Street:      unique.Make(street),
			HouseNumber: unique.Make(house),
			City:        unique.Make(city),
			Region:      unique.Make(""),
			Weight:      weight,
			Type:        typ,
		},
	}
}

// writePointsCache saves a v2 cache with the given points and zones to a
// temporary file and returns its path.
func writePointsCache(t *testing.T, points []cachemodel.Point, zones []cachemodel.Zone) string {
	t.Helper()

	items := make([]cachemodel.Item, 0, len(points))
	for _, point := range points {
		items = append(items, cachemodel.Item{Kind: cachemodel.ItemPoint, Point: point})
	}

	path := filepath.Join(t.TempDir(), "points_test.rgc")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	err = cachesaver.SaveV2(slices.Values(items), slices.Values(zones), cachemodel.Metadata{
		Version:     2,
		Locale:      "en",
		DateCreated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}, file)
	if err != nil {
		t.Fatalf("save cache: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close cache: %v", err)
	}
	return path
}

func TestPointsInRadiusMemory(t *testing.T) {
	points := []cachemodel.Point{
		pointsTestPoint(0, 0, 10, "Alpha", "1", "Town", cachemodel.GeoObjectBuilding),
		pointsTestPoint(0.002, 0, 10, "Beta", "2", "Town", cachemodel.GeoObjectBuilding),
		pointsTestPoint(0.004, 0, 5, "Gamma", "", "", cachemodel.GeoObjectRoad),
		pointsTestPoint(0.006, 0, 3, "", "", "", cachemodel.GeoObjectArea),
		pointsTestPoint(0.5, 0, 10, "Far", "", "", cachemodel.GeoObjectBuilding),
	}
	g := NewGeoCoderFromPoints(points)

	all, total := g.PointsInRadius(0, 0, 0.01, 100, PointKindsAll)
	if total != 4 || len(all) != 4 {
		t.Fatalf("expected 4 points (total 4), got %d (total %d)", len(all), total)
	}
	wantStreets := []string{"Alpha", "Beta", "Gamma", ""}
	for i, want := range wantStreets {
		if all[i].Info.Street != want {
			t.Errorf("point[%d] street = %q, want %q (order must be nearest-first)", i, all[i].Info.Street, want)
		}
	}
	if all[2].Kind != PointKindRoad {
		t.Errorf("point[2] kind = %v, want road", all[2].Kind)
	}
	if all[3].Kind != PointKindArea {
		t.Errorf("point[3] kind = %v, want area", all[3].Kind)
	}

	roads, total := g.PointsInRadius(0, 0, 0.01, 100, PointKindRoad)
	if total != 1 || len(roads) != 1 || roads[0].Info.Street != "Gamma" {
		t.Fatalf("road filter returned %d points (total %d)", len(roads), total)
	}

	limited, total := g.PointsInRadius(0, 0, 0.01, 2, PointKindsAll)
	if total != 4 || len(limited) != 2 {
		t.Fatalf("limit 2 returned %d points (total %d), want 2 of 4", len(limited), total)
	}
	if limited[0].Info.Street != "Alpha" || limited[1].Info.Street != "Beta" {
		t.Errorf("limit must keep the nearest points, got %q, %q", limited[0].Info.Street, limited[1].Info.Street)
	}

	near, total := g.PointsInRadius(0, 0, 0.003, 100, PointKindsAll)
	if total != 2 || len(near) != 2 {
		t.Fatalf("radius 0.003 returned %d points (total %d), want 2", len(near), total)
	}

	none, total := g.PointsInRadius(0, 0, 0.01, 100, 0)
	if total != 0 || len(none) != 0 {
		t.Fatalf("empty kind mask returned %d points (total %d), want none", len(none), total)
	}
}

func TestPointsInRadiusBorderFallback(t *testing.T) {
	square := func(name string, typ cachemodel.ZoneType) cachemodel.Zone {
		return cachemodel.Zone{
			Type:    typ,
			Name:    unique.Make(name),
			Bounds:  orb.Bound{Min: orb.Point{-1, -1}, Max: orb.Point{1, 1}},
			Polygon: orb.MultiPolygon{orb.Polygon{orb.Ring{{-1, -1}, {1, -1}, {1, 1}, {-1, 1}, {-1, -1}}}},
		}
	}

	points := []cachemodel.Point{
		pointsTestPoint(0.001, 0.001, 10, "Main", "7", "Town", cachemodel.GeoObjectBuilding),
		pointsTestPoint(-0.002, 0, 5, "Ring Road", "", "", cachemodel.GeoObjectRoad),
	}
	path := writePointsCache(t, points, []cachemodel.Zone{
		square("Test Region", cachemodel.ZoneRegion),
		square("Testland", cachemodel.ZoneCountry),
	})

	check := func(t *testing.T, g Geocoder) {
		t.Helper()

		listed, total := g.PointsInRadius(0, 0, 0.01, 100, PointKindsAll)
		if total != 2 || len(listed) != 2 {
			t.Fatalf("expected 2 points (total 2), got %d (total %d)", len(listed), total)
		}
		if listed[0].Info.Street != "Main" || listed[0].Info.HouseNumber != "7" {
			t.Errorf("nearest point = %+v, want Main 7", listed[0].Info)
		}
		if listed[1].Info.Street != "Ring Road" || listed[1].Kind != PointKindRoad {
			t.Errorf("second point = %+v (kind %v), want Ring Road road", listed[1].Info, listed[1].Kind)
		}
		for i, point := range listed {
			if point.Info.Region != "Test Region" || point.Info.Country != "Testland" {
				t.Errorf("point[%d] border fallback = %q / %q, want Test Region / Testland",
					i, point.Info.Region, point.Info.Country)
			}
		}
	}

	t.Run("memory", func(t *testing.T) {
		g, err := LoadGeoCoderFromFile(path)
		if err != nil {
			t.Fatalf("load cache: %v", err)
		}
		check(t, g)
	})

	t.Run("disk", func(t *testing.T) {
		g, err := LoadGeoCoderFromFileDisk(path)
		if err != nil {
			t.Fatalf("load disk cache: %v", err)
		}
		t.Cleanup(func() { _ = g.Close() })
		check(t, g)
	})
}
