package bordertree

import (
	"math"
	"sync"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/orb/simplify"
	"github.com/tidwall/qtree"
)

// Selection controls which containing border QueryPoint chooses when several
// polygons overlap. First keeps the original first-match behavior; Smallest
// chooses the smallest containing polygon, with the border key breaking ties.
// Smallest makes region/place lookups independent of insertion order.
type Selection uint8

const (
	// First returns the first containing border visited by the spatial index.
	First Selection = iota
	// Smallest returns the containing border with the smallest polygon area.
	// Ties are broken by the border key and then by insertion order.
	Smallest
	// Largest returns the containing border with the largest polygon area.
	// Ties are broken by the border key and then by insertion order.
	Largest
)

// Make Data type as small as possible, it is included in tree elements, big data will cause a lot of memory copying. Prefer pointers or unique.Handle if possible.
type BorderTree[Data any] struct {
	selection Selection

	mu        sync.RWMutex
	idCounter uint64
	borders   []border[Data]
	qt        qtree.QTree
}

func NewBorderTree[Data any]() *BorderTree[Data] {
	return NewBorderTreeWithSelection[Data](First)
}

func NewBorderTreeWithSelection[Data any](selection Selection) *BorderTree[Data] {
	return &BorderTree[Data]{selection: selection}
}

type border[D any] struct {
	Data D
	// Key is a stable identifier used to break equal-area ties in Smallest mode.
	Key  string
	Area float64

	OversimplifiedPolygon orb.MultiPolygon
	OversimplifiedBounds  [][]orb.Bound
	Polygon               orb.MultiPolygon
	Bounds                [][]orb.Bound
}

func (bt *BorderTree[Data]) InsertBorder(data Data, b orb.MultiPolygon) {
	bt.InsertBorderKeyed(data, "", b)
}

// InsertBorderKeyed inserts a border with a stable key. The key only matters
// for equal-area overlaps in Smallest mode and has no effect in First mode.
func (bt *BorderTree[Data]) InsertBorderKeyed(data Data, key string, b orb.MultiPolygon) {
	bound := b.Bound()
	area := math.Abs(planar.Area(b))

	bt.mu.Lock()
	defer bt.mu.Unlock()

	oversimplified := simplify.VisvalingamKeep(10).MultiPolygon(b.Clone())
	polygon := b.Clone()
	bt.borders = append(bt.borders, border[Data]{
		Data:                  data,
		Key:                   key,
		Area:                  area,
		OversimplifiedPolygon: oversimplified,
		OversimplifiedBounds:  ringBounds(oversimplified),
		Polygon:               polygon,
		Bounds:                ringBounds(polygon),
	})
	bt.qt.Insert(bound.Min, bound.Max, bt.idCounter)
	bt.idCounter++
}

func (bt *BorderTree[Data]) QueryPoint(point orb.Point) (Data, bool) {
	bt.mu.RLock()
	defer bt.mu.RUnlock()

	var (
		out      Data
		found    bool
		bestArea float64
		bestKey  string
		bestID   uint64
	)

	bt.qt.Search(point, point, func(_, _ [2]float64, data interface{}) bool {
		id := data.(uint64)
		b := &bt.borders[id]

		if !multiPolygonContains(b.OversimplifiedPolygon, b.OversimplifiedBounds, point) {
			return true
		}
		if !multiPolygonContains(b.Polygon, b.Bounds, point) {
			return true
		}

		if bt.selection == First {
			out = b.Data
			found = true
			return false
		}

		if found {
			// Equal-area candidates use the stable key, then insertion order,
			// so the result is independent of the spatial index's traversal
			// order.
			if bt.selection == Smallest {
				if b.Area > bestArea {
					return true
				}
			} else if b.Area < bestArea {
				return true
			}
			if b.Area == bestArea {
				if b.Key > bestKey || (b.Key == bestKey && id >= bestID) {
					return true
				}
			}
		}

		out = b.Data
		found = true
		bestArea = b.Area
		bestKey = b.Key
		bestID = id
		return true
	})

	return out, found
}
