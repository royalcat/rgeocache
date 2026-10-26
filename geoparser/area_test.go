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
