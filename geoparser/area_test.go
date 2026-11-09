package geoparser

import (
	"reflect"
	"testing"

	"github.com/paulmach/orb"
)

func TestFillPolygonWithPointsDeterministic(t *testing.T) {
	poly := orb.MultiPolygon{orb.Polygon{orb.Ring{
		orb.Point{0, 0},
		orb.Point{0.1, 0},
		orb.Point{0.1, 0.1},
		orb.Point{0, 0.1},
		orb.Point{0, 0},
	}}}

	first := fillPolygonWithPoints(poly, 0.01/2, 42)
	second := fillPolygonWithPoints(poly, 0.01/2, 42)
	if len(first) == 0 {
		t.Fatal("expected sampled points")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same seed produced different area points")
	}

	other := fillPolygonWithPoints(poly, 0.01/2, 43)
	if reflect.DeepEqual(first, other) {
		t.Fatal("different seeds produced identical area points")
	}
}

func square(x0, y0, size float64) orb.Polygon {
	return orb.Polygon{orb.Ring{
		{x0, y0}, {x0 + size, y0}, {x0 + size, y0 + size}, {x0, y0 + size}, {x0, y0},
	}}
}

// A continent-sized relation must stay within the candidate budget instead of
// sampling a 40°x40° box at 0.005° (64M cells).
func TestFillPolygonWithPointsHugeRelationIsBounded(t *testing.T) {
	poly := orb.MultiPolygon{square(0, 0, 40)}

	pts := fillPolygonWithPoints(poly, 0.01/2, 7)
	if len(pts) == 0 {
		t.Fatal("expected points for a huge polygon")
	}
	if len(pts) > 2*maxPoissonCandidates {
		t.Fatalf("got %d points, budget is %d candidates", len(pts), maxPoissonCandidates)
	}
	for _, p := range pts {
		if !(p[0] >= 0 && p[0] <= 40 && p[1] >= 0 && p[1] <= 40) {
			t.Fatalf("point %v outside polygon", p)
		}
	}
	again := fillPolygonWithPoints(poly, 0.01/2, 7)
	if !reflect.DeepEqual(pts, again) {
		t.Fatal("huge relation sampling is not deterministic")
	}
}

// Two small islands far apart must not pay for the empty sea in between.
func TestFillPolygonWithPointsSkipsEmptyBoundingBox(t *testing.T) {
	poly := orb.MultiPolygon{square(0, 0, 0.1), square(60, 60, 0.1)}

	pts := fillPolygonWithPoints(poly, 0.01/2, 1)
	var a, b int
	for _, p := range pts {
		switch {
		case p[0] < 1:
			a++
		case p[0] > 59:
			b++
		default:
			t.Fatalf("point %v in the gap", p)
		}
	}
	if a == 0 || b == 0 {
		t.Fatalf("expected points on both islands, got %d and %d", a, b)
	}
}
