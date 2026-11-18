package bordertree

import (
	"math"

	"github.com/paulmach/orb"
)

// ringIndex limits the edges tested by ringContains to those whose x-range
// covers the query point's x. ringContains toggles a parity bit per crossing
// and returns early on an "on" hit, so iterating a subset of edges in a
// different order gives exactly the same answer as long as every edge that
// could cross the vertical line at p.x is included.
//
// Edges are bucketed by [min(x), max(x)]. Wide edges spanning more than
// wideBucketLimit buckets are kept in a separate list checked for every query,
// which bounds both index size and per-query work.
type ringIndex struct {
	ring orb.Ring
	b    orb.Bound

	loX, invW float64
	nBuckets  int

	buckets [][]uint32 // bucket -> indexes into edges
	wide    []uint32   // edges spanning too many buckets

	// edges stores endpoint indexes into ring: (a, b) means the segment
	// ring[a] -> ring[b]. The closing edge is (0, last).
	edges []edgeRef
}

type edgeRef struct{ a, b uint32 }

const (
	ringBucketTarget   = 8  // target edges per bucket
	ringBucketMin      = 16 // minimum buckets for a ring
	ringBucketMax      = 512
	wideBucketLimit    = 16
	ringMinIndexLength = 24 // smaller rings use the linear scan
)

// buildRingIndex returns an x-bucket index for r, or nil when r is too small to
// benefit. bound must be r.Bound().
func buildRingIndex(r orb.Ring, bound orb.Bound) *ringIndex {
	if len(r) < ringMinIndexLength || bound.IsZero() {
		return nil
	}
	width := bound.Max[0] - bound.Min[0]
	if width <= 0 || math.IsInf(width, 0) {
		return nil
	}

	// One edge per consecutive pair plus the closing edge, matching the
	// iteration in ringContains exactly.
	n := len(r)
	ri := &ringIndex{
		ring:  r,
		b:     bound,
		loX:   bound.Min[0],
		invW:  1 / width,
		edges: make([]edgeRef, 0, n),
		wide:  nil,
	}
	nBuckets := n / ringBucketTarget
	if nBuckets < ringBucketMin {
		nBuckets = ringBucketMin
	}
	if nBuckets > ringBucketMax {
		nBuckets = ringBucketMax
	}
	ri.nBuckets = nBuckets

	addEdge := func(a, b uint32) {
		ei := uint32(len(ri.edges))
		ri.edges = append(ri.edges, edgeRef{a, b})
		lo, hi := r[a][0], r[b][0]
		if lo > hi {
			lo, hi = hi, lo
		}
		b0, b1 := ri.bucket(lo), ri.bucket(hi)
		if b1-b0 > wideBucketLimit {
			ri.wide = append(ri.wide, ei)
			return
		}
		for k := b0; k <= b1; k++ {
			if ri.buckets == nil {
				ri.buckets = make([][]uint32, nBuckets)
			}
			ri.buckets[k] = append(ri.buckets[k], ei)
		}
	}

	addEdge(0, uint32(n-1))
	for i := 0; i < n-1; i++ {
		addEdge(uint32(i), uint32(i+1))
	}
	return ri
}

func (ri *ringIndex) bucket(x float64) int {
	k := int((x - ri.loX) * float64(ri.nBuckets) * ri.invW)
	if k < 0 {
		return 0
	}
	if k >= ri.nBuckets {
		return ri.nBuckets - 1
	}
	return k
}

// contains is ringContains restricted to edges whose x-range covers p.x.
func (ri *ringIndex) contains(p orb.Point) bool {
	if len(ri.ring) < 3 || !ri.b.Contains(p) {
		return false
	}

	c := false
	check := func(ei uint32) bool {
		e := ri.edges[ei]
		inter, on := rayIntersect(p, ri.ring[e.a], ri.ring[e.b])
		if on {
			return true
		}
		if inter {
			c = !c
		}
		return false
	}

	for _, ei := range ri.wide {
		if check(ei) {
			return true
		}
	}
	if ri.buckets != nil {
		for _, ei := range ri.buckets[ri.bucket(p[0])] {
			if check(ei) {
				return true
			}
		}
	}
	return c
}

// indexedMultiPolygon holds the x-bucket index of each full-resolution ring;
// a nil entry means that ring is small and uses the linear scan.
type indexedMultiPolygon [][]*ringIndex

// buildIndexedMultiPolygon indexes every ring of a full-resolution polygon.
func buildIndexedMultiPolygon(mp orb.MultiPolygon, bounds [][]orb.Bound) indexedMultiPolygon {
	out := make(indexedMultiPolygon, len(mp))
	for i, poly := range mp {
		out[i] = make([]*ringIndex, len(poly))
		for j, r := range poly {
			if i < len(bounds) && j < len(bounds[i]) {
				out[i][j] = buildRingIndex(r, bounds[i][j])
			}
		}
	}
	return out
}

// multiPolygonContainsIndexed is multiPolygonContains using x-bucket indexes
// for the full-resolution rings.
func multiPolygonContainsIndexed(mp orb.MultiPolygon, bounds [][]orb.Bound, idx indexedMultiPolygon, p orb.Point) bool {
	for i, poly := range mp {
		if len(poly) == 0 || len(bounds[i]) != len(poly) {
			continue
		}
		if !bounds[i][0].Contains(p) {
			continue
		}

		inside := false
		if i < len(idx) && idx[i][0] != nil {
			inside = idx[i][0].contains(p)
		} else {
			inside = ringContains(poly[0], bounds[i][0], p)
		}
		if !inside {
			continue
		}

		inHole := false
		for j := 1; j < len(poly); j++ {
			holeInside := false
			if i < len(idx) && idx[i][j] != nil {
				holeInside = idx[i][j].contains(p)
			} else {
				holeInside = bounds[i][j].Contains(p) && ringContains(poly[j], bounds[i][j], p)
			}
			if holeInside {
				inHole = true
				break
			}
		}
		if !inHole {
			return true
		}
	}
	return false
}
