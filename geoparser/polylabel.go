package geoparser

import (
	"math"

	"github.com/paulmach/orb"
)

type Cell struct {
	X   float64 // Cell center X
	Y   float64 // Cell center Y
	H   float64 // Half cell size
	D   float64 // Distance from cell center to polygon
	Max float64 // max distance to polygon within a cell
}

func NewCell(x float64, y float64, h float64, p orb.Ring) *Cell {
	var d float64 = pointToPolygonDist(x, y, p)
	return &Cell{
		x,
		y,
		h,
		d,
		d + h*math.Sqrt2,
	}
}

func pointToPolygonDist(x float64, y float64, p orb.Ring) float64 {
	inside := false
	minDistSq := math.MaxFloat64

	for i, length, j := 0, len(p), len(p)-1; i < length; i, j = i+1, i {
		a := p[i]
		b := p[j]

		if ((a[1] > y) != (b[1] > y)) &&
			(x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0]) {
			inside = !inside
		}

		minDistSq = math.Min(minDistSq, getSegDistSq(x, y, a, b))
	}

	if inside {
		return math.Sqrt(minDistSq)
	} else {
		return -1 * math.Sqrt(minDistSq)
	}
}

func getCentroidCell(points orb.Ring) *Cell {
	var area = 0.0
	var x = 0.0
	var y = 0.0

	for i, length, j := 0, len(points), len(points)-1; i < length; i, j = i+1, i {
		var a = points[i]
		var b = points[j]
		var f = a[0]*b[1] - b[0]*a[1]
		x += (a[0] + b[0]) * f
		y += (a[1] + b[1]) * f
		area += f * 3
	}

	if area == 0 {
		return NewCell(points[0][0], points[0][1], 0, points)
	} else {
		return NewCell(x/area, y/area, 0, points)
	}
}

func getSegDistSq(px float64, py float64, a orb.Point, b orb.Point) float64 {
	var x = a[0]
	var y = a[1]
	var dx = b[0] - x
	var dy = b[1] - y

	if (dx != 0) || (dy != 0) {

		var t = ((px-x)*dx + (py-y)*dy) / (dx*dx + dy*dy)

		if t > 1 {
			x = b[0]
			y = b[1]

		} else if t > 0 {
			x += dx * t
			y += dy * t
		}
	}

	dx = px - x
	dy = py - y

	return dx*dx + dy*dy
}
