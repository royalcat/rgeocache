package geoparser

import (
	"math"

	"github.com/paulmach/orb"
)

type ring orb.Ring

func Polylabel(ring orb.Ring, precision float64) (x, y float64) {
	minX, minY, maxX, maxY := math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64
	for _, p := range ring {
		minX = math.Min(minX, p[0])
		minY = math.Min(minY, p[1])
		maxX = math.Max(maxX, p[0])
		maxY = math.Max(maxY, p[1])
	}

	var width = maxX - minX
	var height = maxY - minY
	var cellSize = math.Min(width, height)
	var h = cellSize / 2

	cellQueue := newPriorityQueue()

	if cellSize == 0 {
		return minX, minY
	}

	for x := minX; x < maxX; x += cellSize {
		for y := minY; y < maxY; y += cellSize {
			cell := NewCell(x+h, y+h, h, ring)
			cellQueue.Insert(*cell, cell.Max)
		}
	}

	bestCell := getCentroidCell(ring)

	var bboxCell = NewCell(minX+width/2, minY+height/2, 0, ring)
	if bboxCell.D > bestCell.D {
		bestCell = bboxCell
	}

	numProbes := cellQueue.Len()

	for cellQueue.Len() != 0 {
		cellInterface, _ := cellQueue.Pop()
		cell := cellInterface.(Cell)

		// update the best cell if we found a better one
		if cell.D > bestCell.D {
			bestCell = &cell
		}

		// do not drill down further if there's no chance of a better solution
		if cell.Max-bestCell.D <= precision {
			continue
		}

		// split the cell into four cells
		h = cell.H / 2
		cells := []*Cell{
			NewCell(cell.X-h, cell.Y-h, h, ring),
			NewCell(cell.X+h, cell.Y-h, h, ring),
			NewCell(cell.X-h, cell.Y+h, h, ring),
			NewCell(cell.X+h, cell.Y+h, h, ring),
		}

		for _, ncell := range cells {
			cellQueue.Insert(*ncell, ncell.Max)
		}

		numProbes += 4
	}

	return bestCell.X, bestCell.Y
}

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
