package boundstree_test

import (
	"testing"

	"github.com/paulmach/orb"
	"github.com/royalcat/rgeocache/boundstree"
)

func TestSimplePoint(t *testing.T) {
	bt := boundstree.NewBoundTree()

	b1 := orb.MultiPoint{orb.Point{0, 0}, orb.Point{1, 1}}.Bound()
	b2 := orb.MultiPoint{orb.Point{0, 0}, orb.Point{-1, -1}}.Bound()

	bt.InsertBorder("1", b1)
	bt.InsertBorder("2", b2)
	r := bt.QueryPoint(orb.Point{0.5, 0.5})
	if r != "1" {
		t.Fatalf("expected 1, got %s", r)
	}

	r = bt.QueryPoint(orb.Point{-0.5, -0.5})
	if r != "2" {
		t.Fatalf("expected 2, got %s", r)
	}
}

func FuzzInBoundCheck(f *testing.F) {

}
