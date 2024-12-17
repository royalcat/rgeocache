package boundstree

import (
	"math"

	"github.com/paulmach/orb"
	// "github.com/paulmach/orb/quadtree"
	// "github.com/JamesLMilner/quadtree-go"
	"github.com/s0rg/quadtree"
)

type BoundTree struct {
	qt *quadtree.Tree[string]
}

const offset = 90.0

func NewBoundTree() *BoundTree {
	return &BoundTree{
		qt: quadtree.New[string](180, 180, 4),
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

func (bt *BoundTree) InsertBorder(name string, b orb.Bound) {
	tl := b.LeftTop()
	br := b.RightBottom()
	x := offset + tl.X()
	y := offset + tl.Y()
	w := math.Abs(br.X() - tl.X())
	h := math.Abs(br.Y() - tl.Y())
	bt.qt.Add(x, y, w, h, name)
}

func (bt *BoundTree) QueryPoint(point orb.Point) string {
	out := ""

	bt.qt.KNearest(offset+point[0], offset+point[1], 1, 1, func(x, y, w, h float64, value string) {
		bound := orb.MultiPoint{orb.Point{x, y}, orb.Point{x + w, y + h}}.Bound()
		if bound.Contains(point) {
			out = value
		}
	})

	return out
}
