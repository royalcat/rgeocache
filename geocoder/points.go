package geocoder

import (
	"container/heap"
	"sort"
	"unique"

	"github.com/paulmach/orb"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/internal/bordertree"
)

// PointKind is the object kind of a cached point, as reported by
// PointsInRadius. Values are a bitmask, so callers can OR them into a filter.
type PointKind uint8

const (
	PointKindBuilding PointKind = 1 << iota
	PointKindRoad
	PointKindArea

	// PointKindsAll matches every kind.
	PointKindsAll = PointKindBuilding | PointKindRoad | PointKindArea
)

// String returns the kind as it appears in API responses.
func (k PointKind) String() string {
	switch k {
	case PointKindBuilding:
		return "building"
	case PointKindRoad:
		return "road"
	case PointKindArea:
		return "area"
	default:
		return "unknown"
	}
}

// PointKindFromWeight derives the object kind from a point's generator weight:
// 5 is a road (highways are resampled), 3/2 are the industrial/protected area
// sub-kinds, everything else is a building. It is the Go-side fallback because
// the in-memory geoInfo does not carry the explicit type byte.
func PointKindFromWeight(weight uint8) PointKind {
	switch weight {
	case 5:
		return PointKindRoad
	case 3, 2:
		return PointKindArea
	default:
		return PointKindBuilding
	}
}

// PointInRadius is one cache point returned by a radius query.
type PointInRadius struct {
	// X is longitude and Y is latitude, in degrees.
	X, Y float64
	// Info is the point's address; Region and Country are filled from the
	// border trees when the stored record left them empty, mirroring what Find
	// reports for a lookup at the point's location.
	Info geomodel.Info
	// Kind is the point's object kind, derived from its weight.
	Kind PointKind
}

// fillRegionCountry fills region and country from the border trees when the
// point's own record has none. Same rule Find applies to a lookup.
func fillRegionCountry(info *geomodel.Info, regions, countries *bordertree.BorderTree[unique.Handle[string]], x, y float64) {
	if info.Region == "" && regions != nil {
		if region, ok := regions.QueryPoint(orb.Point{x, y}); ok {
			info.Region = region.Value()
		}
	}
	if info.Country == "" && countries != nil {
		if country, ok := countries.QueryPoint(orb.Point{x, y}); ok {
			info.Country = country.Value()
		}
	}
}

// radiusCollector keeps the nearest `limit` hits seen during a spatial
// traversal with bounded memory, while counting every matching hit so callers
// can tell truncation from an exact result.
type radiusCollector[T any] struct {
	limit int
	hits  radiusHeap[T]
	total int
}

type radiusHit[T any] struct {
	dist2 float64
	x, y  float64
	data  T
}

// radiusHeap is a max-heap whose greatest element is the worst hit, so the
// collector can drop it once `limit` hits are kept.
type radiusHeap[T any] []radiusHit[T]

// hitLess orders hits nearest-first, with exact distance ties broken by
// longitude then latitude. The total order makes both the retained set of a
// bounded collector and the returned order independent of traversal order.
func hitLess[T any](a, b radiusHit[T]) bool {
	if a.dist2 != b.dist2 {
		return a.dist2 < b.dist2
	}
	if a.x != b.x {
		return a.x < b.x
	}
	return a.y < b.y
}

func (h radiusHeap[T]) Len() int { return len(h) }

// Less keeps the heap's root at the worst (farthest) hit.
func (h radiusHeap[T]) Less(i, j int) bool { return hitLess(h[j], h[i]) }
func (h radiusHeap[T]) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *radiusHeap[T]) Push(v any) { *h = append(*h, v.(radiusHit[T])) }

func (h *radiusHeap[T]) Pop() any {
	old := *h
	n := len(old)
	hit := old[n-1]
	*h = old[:n-1]
	return hit
}

func newRadiusCollector[T any](limit int) *radiusCollector[T] {
	return &radiusCollector[T]{limit: limit}
}

// add records a matching hit. dist2 must be finite.
func (c *radiusCollector[T]) add(x, y, dist2 float64, data T) {
	c.total++
	if c.limit <= 0 {
		return
	}
	hit := radiusHit[T]{dist2: dist2, x: x, y: y, data: data}
	if len(c.hits) < c.limit {
		heap.Push(&c.hits, hit)
		return
	}
	if hitLess(hit, c.hits[0]) {
		c.hits[0] = hit
		heap.Fix(&c.hits, 0)
	}
}

// sorted returns the collected hits nearest-first.
func (c *radiusCollector[T]) sorted() []radiusHit[T] {
	hits := c.hits
	sort.Slice(hits, func(i, j int) bool { return hitLess(hits[i], hits[j]) })
	return hits
}
