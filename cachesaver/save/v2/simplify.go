package savev2

import (
	"fmt"
	"math"
)

// Road graph simplification constants. The goal is to keep the road geometry
// faithful — a vertex is dropped only when it lies within about a centimetre of
// the straight chord — while removing redundant OSM shape nodes from the cache
// and keeping road fallback points for reverse geocoding no farther apart than
// the old 150 m resampling did.
const (
	// graphSimplifyEpsilon is the perpendicular tolerance in degrees, weighted
	// by cos(latitude) so it is isotropic; 1e-7° is about one centimetre.
	graphSimplifyEpsilon = 1e-7
	// graphMaxEdgeMeters caps the length of every kept graph edge wherever OSM
	// provides enough shape nodes, so at least one node per 150 m is retained.
	// A raw OSM segment longer than the cap cannot be split and is left as is.
	graphMaxEdgeMeters = 150.0
	// metersPerDegreeLat is the mean length of one degree of latitude.
	metersPerDegreeLat = 111_320.0
)

// graphPoint is a plain projected vertex used by the simplifier.
type graphPoint struct{ x, y float64 }

// simplifyGraph removes near-collinear interior nodes from the road graph
// before the cache is written.
//
// Connected edges that share street, name, class and oneway form a chain.
// Every chain is simplified with a Douglas-Peucker pass: an interior vertex is
// dropped only when it lies within graphSimplifyEpsilon of the straight chord
// and every resulting edge stays within graphMaxEdgeMeters (a longer straight
// span is split at the vertex closest to its middle; a raw OSM segment longer
// than the cap is left untouched). Chains break at nodes whose degree differs
// from two and where edge properties change, so junctions, way ends and
// property boundaries are always kept — the graph topology does not change.
//
// It returns the filtered point slice (non-road points untouched; a road point
// is kept when an edge still references it or when it has no edges at all), the
// rewritten edges and the node id -> point index mapping for the filtered
// slice.
func simplifyGraph(points []rawPoint, edges []rawEdge, maxGraphNode uint32) ([]rawPoint, []rawEdge, []uint32, error) {
	nodeCount := int(maxGraphNode) + 1

	nodeOrig := make([]uint32, nodeCount)
	for i := range points {
		if graphNode := points[i].graphNode; graphNode != 0 {
			nodeOrig[graphNode] = uint32(i)
		}
	}

	// Per-node degree and a CSR adjacency over the edges, so chains can be
	// walked without a map entry per edge.
	deg := make([]uint32, nodeCount)
	for i := range edges {
		e := &edges[i]
		if int(e.fromNode) >= nodeCount || int(e.toNode) >= nodeCount {
			return nil, nil, nil, fmt.Errorf(
				"savev2: graph edge %d -> %d references a node without an emitted point",
				e.fromNode, e.toNode,
			)
		}
		deg[e.fromNode]++
		deg[e.toNode]++
	}
	adjStart := make([]uint32, nodeCount+1)
	var total uint32
	for i, d := range deg {
		adjStart[i] = total
		total += d
	}
	adjStart[nodeCount] = total
	adjEdge := make([]uint32, total)
	fill := make([]uint32, nodeCount)
	for i := range edges {
		e := &edges[i]
		adjEdge[adjStart[e.fromNode]+fill[e.fromNode]] = uint32(i)
		fill[e.fromNode]++
		adjEdge[adjStart[e.toNode]+fill[e.toNode]] = uint32(i)
		fill[e.toNode]++
	}
	fill = nil

	visited := make([]uint64, (len(edges)+63)/64)
	isVisited := func(i int) bool { return visited[i>>6]&(1<<(uint(i)&63)) != 0 }
	markVisited := func(i int) { visited[i>>6] |= 1 << (uint(i) & 63) }

	kept := make([]uint64, (nodeCount+63)/64)
	markKept := func(n uint32) { kept[n>>6] |= 1 << (n & 63) }
	isKept := func(n uint32) bool { return kept[n>>6]&(1<<(n&63)) != 0 }

	type edgeKey struct {
		street, name  string
		class, oneway uint8
	}

	// continuation returns the unused edge that continues a chain through the
	// degree-two node `at` and shares the chain's properties.
	continuation := func(at, current uint32, key edgeKey) (uint32, bool) {
		if deg[at] != 2 {
			return 0, false
		}
		for _, e := range adjEdge[adjStart[at]:adjStart[at+1]] {
			if e == current || isVisited(int(e)) {
				continue
			}
			ee := &edges[e]
			if ee.street != key.street || ee.name != key.name ||
				ee.class != key.class || ee.oneway != key.oneway {
				return 0, false
			}
			return e, true
		}
		return 0, false
	}

	type placedEdge struct {
		edge uint32
		// dir is true when the edge is placed along its stored from -> to.
		dir bool
	}

	newEdges := make([]rawEdge, 0, len(edges))
	for start := range edges {
		if isVisited(start) {
			continue
		}
		markVisited(start)
		key := edgeKey{
			street: edges[start].street,
			name:   edges[start].name,
			class:  edges[start].class,
			oneway: edges[start].oneway,
		}

		order := []placedEdge{{edge: uint32(start), dir: true}}

		for {
			last := order[len(order)-1]
			at := edges[last.edge].toNode
			if !last.dir {
				at = edges[last.edge].fromNode
			}
			next, ok := continuation(at, last.edge, key)
			if !ok {
				break
			}
			markVisited(int(next))
			order = append(order, placedEdge{edge: next, dir: edges[next].fromNode == at})
		}

		// Extend backwards: edges that run into the chain head are collected
		// and prepended in reverse order at the end.
		var back []placedEdge
		head := order[0]
		for {
			at := edges[head.edge].fromNode
			if !head.dir {
				at = edges[head.edge].toNode
			}
			next, ok := continuation(at, head.edge, key)
			if !ok {
				break
			}
			markVisited(int(next))
			head = placedEdge{edge: next, dir: edges[next].toNode == at}
			back = append(back, head)
		}

		chain := make([]placedEdge, 0, len(order)+len(back))
		for i := len(back) - 1; i >= 0; i-- {
			chain = append(chain, back[i])
		}
		chain = append(chain, order...)

		// Directed chains must keep the stored from -> to orientation so the
		// oneway property stays relative to the stored direction. Walking a
		// chain backwards flips every edge's placement.
		if key.oneway != 0 && !chain[0].dir {
			for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
				chain[i], chain[j] = chain[j], chain[i]
			}
			for i := range chain {
				chain[i].dir = !chain[i].dir
			}
		}

		seq := make([]uint32, 0, len(chain)+1)
		for i := range chain {
			from, to := edges[chain[i].edge].fromNode, edges[chain[i].edge].toNode
			if !chain[i].dir {
				from, to = to, from
			}
			if len(seq) == 0 {
				seq = append(seq, from)
			}
			seq = append(seq, to)
		}

		coords := make([]graphPoint, len(seq))
		for i, node := range seq {
			p := &points[nodeOrig[node]]
			coords[i] = graphPoint{x: p.x, y: p.y}
		}

		keepIdx := simplifyChain(coords)
		for _, ki := range keepIdx {
			markKept(seq[ki])
		}
		for i := 1; i < len(keepIdx); i++ {
			from, to := seq[keepIdx[i-1]], seq[keepIdx[i]]
			if from == to {
				continue
			}
			newEdges = append(newEdges, rawEdge{
				fromNode: from,
				toNode:   to,
				street:   key.street,
				name:     key.name,
				class:    key.class,
				oneway:   key.oneway,
			})
		}
	}

	// Filter the point slice in place: a road point survives only when an edge
	// still references it or it has no edges at all.
	filtered := points[:0]
	newNodeOrig := make([]uint32, nodeCount)
	for i := range points {
		p := &points[i]
		if graphNode := p.graphNode; graphNode != 0 {
			if deg[graphNode] != 0 && !isKept(graphNode) {
				continue
			}
			newNodeOrig[graphNode] = uint32(len(filtered))
		}
		filtered = append(filtered, *p)
	}

	return filtered, newEdges, newNodeOrig, nil
}

// simplifyChain returns the ascending indexes of the vertices to keep.
//
// A closed chain (its first position equals its last) has a degenerate DP
// chord, so it is split at the vertex farthest from the start first.
func simplifyChain(pts []graphPoint) []int {
	n := len(pts)
	switch {
	case n == 0:
		return nil
	case n <= 2:
		return []int{0, n - 1}
	}

	keep := make([]bool, n)
	keep[0] = true
	keep[n-1] = true
	if pts[0] == pts[n-1] {
		far := 1
		best := 0.0
		for i := 1; i < n-1; i++ {
			if d := scaledSquaredDistance(pts[i], pts[0]); d > best {
				best = d
				far = i
			}
		}
		keepSpan(pts, 0, far, keep)
		keepSpan(pts, far, n-1, keep)
	} else {
		keepSpan(pts, 0, n-1, keep)
	}

	out := make([]int, 0, 8)
	for i, k := range keep {
		if k {
			out = append(out, i)
		}
	}
	return out
}

// keepSpan marks the vertices to keep inside [a, b], endpoints included.
//
// A span is split at its farthest interior vertex when that vertex deviates by
// more than the tolerance, and at the vertex closest to the middle when the
// straight chord is longer than the edge cap. Splitting always keeps at least
// one interior vertex, so the recursion terminates.
func keepSpan(pts []graphPoint, a, b int, keep []bool) {
	stack := [][2]int{{a, b}}
	for len(stack) > 0 {
		span := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		lo, hi := span[0], span[1]
		keep[lo], keep[hi] = true, true
		if hi <= lo+1 {
			continue
		}

		maxIdx, maxDev := lo, 0.0
		for i := lo + 1; i < hi; i++ {
			if d := pointSegmentDistance(pts[lo], pts[hi], pts[i]); d > maxDev {
				maxDev, maxIdx = d, i
			}
		}
		if maxDev > graphSimplifyEpsilon {
			stack = append(stack, [2]int{lo, maxIdx}, [2]int{maxIdx, hi})
			continue
		}

		if segmentMeters(pts[lo], pts[hi]) > graphMaxEdgeMeters {
			mid, best := lo+1, math.Inf(1)
			for i := lo + 1; i < hi; i++ {
				t := projectionParam(pts[lo], pts[hi], pts[i])
				if d := math.Abs(t - 0.5); d < best {
					best, mid = d, i
				}
			}
			stack = append(stack, [2]int{lo, mid}, [2]int{mid, hi})
		}
	}
}

// lonScale weighs longitude differences so distances are isotropic.
func lonScale(lat float64) float64 {
	return math.Cos(lat * math.Pi / 180)
}

// pointSegmentDistance is the distance of p from the segment a -> b, in degrees
// with longitude weighted by cos(latitude).
func pointSegmentDistance(a, b, p graphPoint) float64 {
	t := projectionParam(a, b, p)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	scale := lonScale((a.y + b.y) * 0.5)
	ax, ay := a.x*scale, a.y
	bx, by := b.x*scale, b.y
	px, py := p.x*scale, p.y
	return math.Hypot(px-(ax+t*(bx-ax)), py-(ay+t*(by-ay)))
}

// projectionParam is the unclamped projection of p onto the a -> b chord.
func projectionParam(a, b, p graphPoint) float64 {
	scale := lonScale((a.y + b.y) * 0.5)
	ax, ay := a.x*scale, a.y
	bx, by := b.x*scale, b.y
	px, py := p.x*scale, p.y
	dx, dy := bx-ax, by-ay
	len2 := dx*dx + dy*dy
	if len2 == 0 {
		return 0
	}
	return ((px-ax)*dx + (py-ay)*dy) / len2
}

// segmentMeters returns the approximate length of a -> b in metres.
func segmentMeters(a, b graphPoint) float64 {
	scale := lonScale((a.y + b.y) * 0.5)
	dx := (b.x - a.x) * metersPerDegreeLat * scale
	dy := (b.y - a.y) * metersPerDegreeLat
	return math.Hypot(dx, dy)
}

// scaledSquaredDistance is a cos(latitude)-weighted squared distance.
func scaledSquaredDistance(a, b graphPoint) float64 {
	scale := lonScale((a.y + b.y) * 0.5)
	dx := (a.x - b.x) * scale
	dy := a.y - b.y
	return dx*dx + dy*dy
}
