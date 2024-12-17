package bordertree_test

import (
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/royalcat/rgeocache/bordertree"
)

func polygonFromBounds(minX, minY, maxX, maxY float64) orb.MultiPolygon {
	return orb.MultiPolygon{orb.Polygon{orb.Ring{
		orb.Point{minX, minY},
		orb.Point{maxX, minY},
		orb.Point{maxX, maxY},
		orb.Point{minX, maxY},
		orb.Point{minX, minY},
	}}}
}

func TestSimpleBounds(t *testing.T) {
	bt := bordertree.NewBorderTree[string]()

	bt.InsertBorder("1", polygonFromBounds(0, 0, 1, 1))
	bt.InsertBorder("2", polygonFromBounds(-1, -1, 0, 0))
	r, ok := bt.QueryPoint(orb.Point{0.5, 0.5})
	if !ok {
		t.Fatalf("expected true, got false")
	}
	if r != "1" {
		t.Fatalf("expected 1, got %s", r)
	}

	r, ok = bt.QueryPoint(orb.Point{-0.5, -0.5})
	if !ok {
		t.Fatalf("expected true, got false")
	}
	if r != "2" {
		t.Fatalf("expected 2, got %s", r)
	}
}

func FuzzSimpleBoundCheck(f *testing.F) {
	const testData = "1"

	f.Add(0.0, 0.0, 1.0, 1.0, 0.5, 0.5)
	f.Add(0.0, 0.0, 1.0, 1.0, 1.5, 1.5)

	f.Fuzz(func(t *testing.T, minX, minY, maxX, maxY, pointX, pointY float64) {
		polygon := polygonFromBounds(minX, minY, maxX, maxY)
		point := orb.Point{pointX, pointY}
		expectOk := planar.MultiPolygonContains(polygon, point)

		bt := bordertree.NewBorderTree[string]()
		bt.InsertBorder(testData, polygon)

		r, ok := bt.QueryPoint(point)
		if expectOk != ok {
			t.Fatalf("expected %v, got %v", expectOk, ok)
		}

		if expectOk && r != testData {
			t.Fatalf("expected %s, got %s", testData, r)
		}
	})
}
