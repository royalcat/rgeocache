package bordertree

import (
	"sync"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/tidwall/qtree"
)

type BorderTree[Data any] struct {
	mu        sync.RWMutex
	idCounter uint64
	borders   []border[Data]
	qt        qtree.QTree
}

const offset = 90.0

func NewBorderTree[Data any]() *BorderTree[Data] {
	return &BorderTree[Data]{}
}

type border[D any] struct {
	Data    D
	Polygon orb.MultiPolygon
}

func (bt *BorderTree[Data]) InsertBorder(data Data, b orb.MultiPolygon) {
	bound := b.Bound()

	bt.mu.Lock()
	defer bt.mu.Unlock()

	bt.borders = append(bt.borders, border[Data]{Data: data, Polygon: b})
	bt.qt.Insert(bound.Min, bound.Max, bt.idCounter)
	bt.idCounter++
}

func (bt *BorderTree[Data]) QueryPoint(point orb.Point) (Data, bool) {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	var out Data
	found := false

	bt.qt.Search(point, point, func(_, _ [2]float64, data interface{}) bool {
		id := data.(uint64)

		if planar.MultiPolygonContains(bt.borders[id].Polygon, point) {
			out = bt.borders[id].Data
			found = true
			return false
		}

		return true
	})

	return out, found
}
