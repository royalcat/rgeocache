package boundstree

import (
	"github.com/paulmach/orb"
	// "github.com/paulmach/orb/quadtree"
	// "github.com/JamesLMilner/quadtree-go"
)

type BoundTree struct {
	qt *Quadtree[string]
}

func NewBoundTree() *BoundTree {
	return &BoundTree{
		qt: &Quadtree[string]{
			Bounds: Bounds[string]{
				X:      0,
				Y:      0,
				Width:  100,
				Height: 100,
			},
			MaxObjects: 10,
			MaxLevels:  4,
			Level:      0,
			Objects:    make([]Bounds[string], 0),
			Nodes:      make([]Quadtree[string], 0),
		},
	}
}

type Border struct {
	Name   string
	Points []*BorderPoint
}

type BorderPoint struct {
	Point  orb.Point
	Border *Border
}

func (bt *BoundTree) InsertBorder(name string, border orb.MultiPolygon) {
	bound := border.Bound()
	bt.qt.Insert(Bounds[string]{
		X:      bound.Min[0],
		Y:      bound.Min[1],
		Width:  bound.Max[0] - bound.Min[0],
		Height: bound.Max[1] - bound.Min[1],
		Data:   name,
	})
}

func (bt *BoundTree) QueryPoint(point orb.Point) string {
	bounds := bt.qt.Retrieve(Bounds[string]{
		X: point[0],
		Y: point[1],
	})

	if len(bounds) == 0 {
		return ""
	}

	return bounds[0].Data
}
