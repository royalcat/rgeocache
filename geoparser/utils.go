package geoparser

import (
	"iter"

	"github.com/fogleman/poissondisc"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
)

func fillPolygonWithPoints(poly orb.MultiPolygon, distance float64) iter.Seq[orb.Point] {
	return func(yield func(orb.Point) bool) {
		bound := poly.Bound()

		// TODO implement this function with iterators
		points := poissondisc.Sample(bound.Min.X(), bound.Min.Y(), bound.Max.X(), bound.Max.Y(), distance, 10, nil)

		for _, p := range points {
			point := orb.Point{p.X, p.Y}
			if planar.MultiPolygonContains(poly, point) {
				if !yield(point) {
					return
				}
			}
		}
	}
}
