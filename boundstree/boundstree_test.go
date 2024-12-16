package boundstree_test

import (
	"testing"

	"github.com/paulmach/orb"
	"github.com/royalcat/rgeocache/boundstree"
)

func TestSimplePoint(t *testing.T) {
	bt := boundstree.NewBoundTree()
	bt.InsertBorder("1", orb.MultiPolygon{orb.Polygon{{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}}})
	bt.InsertBorder("2", orb.MultiPolygon{orb.Polygon{{{-1, -1}, {0, -1}, {0, 0}, {-1, 0}, {-1, -1}}}})
	r := bt.QueryPoint(orb.Point{0.5, 0.5})
	if r != "1" {
		t.Errorf("expected 1, got %s", r)
	}

	r = bt.QueryPoint(orb.Point{-0.5, -0.5})
	if r != "2" {
		t.Errorf("expected 2, got %s", r)
	}
}
