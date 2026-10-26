package bordertree_test

import (
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/royalcat/rgeocache/internal/bordertree"
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

func TestSmallestSelectionIsInsertionOrderIndependent(t *testing.T) {
	large := polygonFromBounds(0, 0, 2, 2)
	small := polygonFromBounds(0.5, 0.5, 1.5, 1.5)

	for _, smallFirst := range []bool{false, true} {
		bt := bordertree.NewBorderTreeWithSelection[string](bordertree.Smallest)
		if smallFirst {
			bt.InsertBorderKeyed("small", "small", small)
			bt.InsertBorderKeyed("large", "large", large)
		} else {
			bt.InsertBorderKeyed("large", "large", large)
			bt.InsertBorderKeyed("small", "small", small)
		}

		got, ok := bt.QueryPoint(orb.Point{1, 1})
		if !ok {
			t.Fatalf("smallFirst=%v: expected a match", smallFirst)
		}
		if got != "small" {
			t.Fatalf("smallFirst=%v: got %q, want small", smallFirst, got)
		}

		got, ok = bt.QueryPoint(orb.Point{0.25, 0.25})
		if !ok {
			t.Fatalf("smallFirst=%v: expected the large polygon", smallFirst)
		}
		if got != "large" {
			t.Fatalf("smallFirst=%v: got %q, want large", smallFirst, got)
		}
	}
}

func TestLargestSelectionIsInsertionOrderIndependent(t *testing.T) {
	large := polygonFromBounds(0, 0, 2, 2)
	small := polygonFromBounds(0.5, 0.5, 1.5, 1.5)

	for _, largeFirst := range []bool{false, true} {
		bt := bordertree.NewBorderTreeWithSelection[string](bordertree.Largest)
		if largeFirst {
			bt.InsertBorderKeyed("large", "large", large)
			bt.InsertBorderKeyed("small", "small", small)
		} else {
			bt.InsertBorderKeyed("small", "small", small)
			bt.InsertBorderKeyed("large", "large", large)
		}

		got, ok := bt.QueryPoint(orb.Point{1, 1})
		if !ok || got != "large" {
			t.Fatalf("largeFirst=%v: got (%q, %v), want (large, true)", largeFirst, got, ok)
		}
	}
}

func TestSmallestSelectionBreaksEqualAreaTiesByKey(t *testing.T) {
	// Same area, overlapping in the middle.
	left := polygonFromBounds(0, 0, 2, 1)
	right := polygonFromBounds(1, 0, 3, 1)

	for _, rightFirst := range []bool{false, true} {
		bt := bordertree.NewBorderTreeWithSelection[string](bordertree.Smallest)
		if rightFirst {
			bt.InsertBorderKeyed("b", "b", right)
			bt.InsertBorderKeyed("a", "a", left)
		} else {
			bt.InsertBorderKeyed("a", "a", left)
			bt.InsertBorderKeyed("b", "b", right)
		}

		got, ok := bt.QueryPoint(orb.Point{1.5, 0.5})
		if !ok {
			t.Fatalf("rightFirst=%v: expected a match", rightFirst)
		}
		if got != "a" {
			t.Fatalf("rightFirst=%v: got %q, want a (smallest key)", rightFirst, got)
		}
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
