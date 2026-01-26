package geoparser

import (
	"github.com/fogleman/poissondisc"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
)

func fillPolygonWithPoints(poly orb.MultiPolygon, distance float64) []orb.Point {
	// 1. Get the bounding box of the polygon
	bound := poly.Bound()
	points := poissondisc.Sample(bound.Min.X(), bound.Min.Y(), bound.Max.X(), bound.Max.Y(), distance, 10, nil)

	// 2. Filter points inside the polygon
	pointsInside := make([]orb.Point, 0)
	for _, p := range points {
		point := orb.Point{p.X, p.Y}
		if planar.MultiPolygonContains(poly, point) {
			pointsInside = append(pointsInside, point)
		}
	}

	return pointsInside
}
