package geocoder

import (
	"slices"
	"testing"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

// TestFindRanking pins the deterministic Find rule: candidates are ranked by
// squared distance divided by squared weight (distance ÷ weight), so a
// building wins at up to twice a road's distance but loses beyond that.
func TestFindRanking(t *testing.T) {
	tests := []struct {
		name   string
		points []cachemodel.Point
		want   string
	}{
		{
			"building within twice the road distance wins",
			[]cachemodel.Point{
				pointsTestPoint(0.002, 0, 5, "Road", "", "", cachemodel.GeoObjectRoad),
				pointsTestPoint(0.003, 0, 10, "Building", "1", "", cachemodel.GeoObjectBuilding),
			},
			"Building",
		},
		{
			"road wins when the building is farther than twice its distance",
			[]cachemodel.Point{
				pointsTestPoint(0.002, 0, 5, "Road", "", "", cachemodel.GeoObjectRoad),
				pointsTestPoint(0.005, 0, 10, "Building", "1", "", cachemodel.GeoObjectBuilding),
			},
			"Road",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGeoCoderFromPoints(tt.points)

			found, ok := g.Find(0, 0)
			if !ok || found.Street != tt.want {
				t.Fatalf("Find = %+v (ok %v), want street %q", found, ok, tt.want)
			}
		})
	}
}

// TestFindBreaksScoreTies checks the tie-break: equal scores go to the smaller
// longitude, then latitude.
func TestFindBreaksScoreTies(t *testing.T) {
	points := []cachemodel.Point{
		pointsTestPoint(0.003, 0, 10, "East", "", "", cachemodel.GeoObjectBuilding),
		pointsTestPoint(-0.003, 0, 10, "West", "", "", cachemodel.GeoObjectBuilding),
	}
	g := NewGeoCoderFromPoints(points)

	found, ok := g.Find(0, 0)
	if !ok || found.Street != "West" {
		t.Fatalf("Find = %+v (ok %v), want the smaller-longitude tie winner %q", found, ok, "West")
	}
}

// TestFindIsIndependentOfPointOrder builds the same cache twice with the
// points in opposite orders — which changes the KD-tree layout and so the
// traversal order — and requires identical answers.
func TestFindIsIndependentOfPointOrder(t *testing.T) {
	forward := []cachemodel.Point{
		pointsTestPoint(0.002, 0, 5, "Road", "", "", cachemodel.GeoObjectRoad),
		pointsTestPoint(0.003, 0.001, 10, "Building", "1", "", cachemodel.GeoObjectBuilding),
		pointsTestPoint(-0.003, 0, 10, "West", "", "", cachemodel.GeoObjectBuilding),
		pointsTestPoint(0.004, 0, 3, "Area", "", "", cachemodel.GeoObjectArea),
	}
	reversed := slices.Clone(forward)
	slices.Reverse(reversed)

	queries := []struct{ lat, lon float64 }{
		{0, 0},
		{0.001, 0.001},
		{0, -0.002},
		{0.002, 0.002},
	}

	for _, q := range queries {
		a, okA := NewGeoCoderFromPoints(forward).Find(q.lat, q.lon)
		b, okB := NewGeoCoderFromPoints(reversed).Find(q.lat, q.lon)
		if okA != okB || a != b {
			t.Errorf("Find(%.4f, %.4f) = %+v (ok %v) vs %+v (ok %v), want the same answer",
				q.lat, q.lon, a, okA, b, okB)
		}
	}
}

// TestFindRankingLoaders covers both cache-backed implementations, which have
// separate traversal loops.
func TestFindRankingLoaders(t *testing.T) {
	points := []cachemodel.Point{
		pointsTestPoint(0.002, 0, 5, "Road", "", "", cachemodel.GeoObjectRoad),
		pointsTestPoint(0.003, 0, 10, "Building", "1", "", cachemodel.GeoObjectBuilding),
		pointsTestPoint(0.005, 0.005, 3, "Area", "", "", cachemodel.GeoObjectArea),
	}
	path := writePointsCache(t, points, nil)

	check := func(t *testing.T, g Geocoder) {
		t.Helper()

		found, ok := g.Find(0, 0)
		if !ok || found.Street != "Building" {
			t.Fatalf("Find = %+v (ok %v), want street %q", found, ok, "Building")
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
