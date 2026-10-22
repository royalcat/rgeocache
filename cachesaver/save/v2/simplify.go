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

// graphEdge is the simplifier's view of one edge: endpoints are graph node ids
// and the property fields are string table ids (compared for equality only).
type graphEdge struct {
	from, to      uint32
	streetID      uint32
	nameID        uint32
	class, oneway uint8
}

// edgeSource abstracts the edge list. The production writer supplies an
// mmap-backed source over the edge spool; tests and the legacy in-memory save
// supply a slice.
type edgeSource interface {
	Len() int
	At(i int) (graphEdge, error)
}

type sliceEdges []graphEdge

func (s sliceEdges) Len() int                    { return len(s) }
func (s sliceEdges) At(i int) (graphEdge, error) { return s[i], nil }

// simplifyGraphCore removes near-collinear interior nodes from the road graph.
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
// nodeCoords is indexed by graph node id. The returned bitmap marks every node
// that survives: a node with no edges is always kept.
func simplifyGraphCore(
	edges edgeSource,
	nodeCoords []graphPoint,
	nodeCount int,
	emit func(graphEdge) error,
) ([]uint64, error) {
	if nodeCount < 0 {
		return nil, fmt.Errorf("savev2: negative graph node count %d", nodeCount)
	}

	// Per-node degree and a CSR adjacency over the edges, so chains can be
	// walked without a map entry per edge.
	deg := make([]uint32, nodeCount)
	for i := 0; i < edges.Len(); i++ {
		e, err := edges.At(i)
		if err != nil {
			return nil, err
		}
		if int(e.from) >= nodeCount || int(e.to) >= nodeCount {
			return nil, fmt.Errorf(
				"savev2: graph edge %d -> %d references a node without an emitted point",
				e.from, e.to,
			)
		}
		deg[e.from]++
		deg[e.to]++
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
	for i := 0; i < edges.Len(); i++ {
		e, err := edges.At(i)
		if err != nil {
			return nil, err
		}
		adjEdge[adjStart[e.from]+fill[e.from]] = uint32(i)
		fill[e.from]++
		adjEdge[adjStart[e.to]+fill[e.to]] = uint32(i)
		fill[e.to]++
	}
	fill = nil

	visited := make([]uint64, (edges.Len()+63)/64)
	isVisited := func(i int) bool { return visited[i>>6]&(1<<(uint(i)&63)) != 0 }
	markVisited := func(i int) { visited[i>>6] |= 1 << (uint(i) & 63) }

	kept := make([]uint64, (nodeCount+63)/64)
	markKept := func(n uint32) { kept[n>>6] |= 1 << (n & 63) }
	isKept := func(n uint32) bool { return kept[n>>6]&(1<<(n&63)) != 0 }

	type edgeKey struct {
		street, name  uint32
		class, oneway uint8
	}

	// continuation returns the unused edge that continues a chain through the
	// degree-two node `at` and shares the chain's properties.
	continuation := func(at, current uint32, key edgeKey) (uint32, bool, error) {
		if deg[at] != 2 {
			return 0, false, nil
		}
		for _, e := range adjEdge[adjStart[at]:adjStart[at+1]] {
			if e == current || isVisited(int(e)) {
				continue
			}
			ee, err := edges.At(int(e))
			if err != nil {
				return 0, false, err
			}
			if ee.streetID != key.street || ee.nameID != key.name ||
				ee.class != key.class || ee.oneway != key.oneway {
				return 0, false, nil
			}
			return e, true, nil
		}
		return 0, false, nil
	}

	type placedEdge struct {
		edge uint32
		// dir is true when the edge is placed along its stored from -> to.
		dir bool
	}

	for start := 0; start < edges.Len(); start++ {
		if isVisited(start) {
			continue
		}
		startEdge, err := edges.At(start)
		if err != nil {
			return nil, err
		}
		markVisited(start)
		key := edgeKey{
			street: startEdge.streetID,
			name:   startEdge.nameID,
			class:  startEdge.class,
			oneway: startEdge.oneway,
		}

		order := []placedEdge{{edge: uint32(start), dir: true}}

		for {
			last := order[len(order)-1]
			lastEdge, err := edges.At(int(last.edge))
			if err != nil {
				return nil, err
			}
			at := lastEdge.to
			if !last.dir {
				at = lastEdge.from
			}
			next, ok, err := continuation(at, last.edge, key)
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			nextEdge, err := edges.At(int(next))
			if err != nil {
				return nil, err
			}
			markVisited(int(next))
			order = append(order, placedEdge{edge: next, dir: nextEdge.from == at})
		}

		// Extend backwards: edges that run into the chain head are collected
		// and prepended in reverse order at the end.
		var back []placedEdge
		head := order[0]
		for {
			headEdge, err := edges.At(int(head.edge))
			if err != nil {
				return nil, err
			}
			at := headEdge.from
			if !head.dir {
				at = headEdge.to
			}
			next, ok, err := continuation(at, head.edge, key)
			if err != nil {
				return nil, err
			}
			if !ok {
				break
			}
			nextEdge, err := edges.At(int(next))
			if err != nil {
				return nil, err
			}
			markVisited(int(next))
			head = placedEdge{edge: next, dir: nextEdge.to == at}
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
			ce, err := edges.At(int(chain[i].edge))
			if err != nil {
				return nil, err
			}
			from, to := ce.from, ce.to
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
			coords[i] = nodeCoords[node]
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
			if err := emit(graphEdge{
				from:     from,
				to:       to,
				streetID: key.street,
				nameID:   key.name,
				class:    key.class,
				oneway:   key.oneway,
			}); err != nil {
				return nil, err
			}
		}
	}

	keep := make([]uint64, (nodeCount+63)/64)
	for n := 0; n < nodeCount; n++ {
		if deg[n] == 0 || isKept(uint32(n)) {
			keep[n>>6] |= 1 << (uint(n) & 63)
		}
	}
	return keep, nil
}

// simplifyGraph is the legacy in-memory entry point used by Save and the unit
// tests. It adapts []rawPoint/[]rawEdge to the shared core.
func simplifyGraph(points []rawPoint, edges []rawEdge, maxGraphNode uint32) ([]rawPoint, []rawEdge, []uint32, error) {
	nodeCount := int(maxGraphNode) + 1

	nodeCoords := make([]graphPoint, nodeCount)
	for i := range points {
		if graphNode := points[i].graphNode; graphNode != 0 {
			nodeCoords[graphNode] = graphPoint{x: points[i].x, y: points[i].y}
		}
	}

	intern := make(map[string]uint32)
	reverse := make([]string, 0, len(intern))
	internID := func(s string) uint32 {
		if id, ok := intern[s]; ok {
			return id
		}
		id := uint32(len(reverse))
		intern[s] = id
		reverse = append(reverse, s)
		return id
	}

	ge := make([]graphEdge, len(edges))
	for i, e := range edges {
		ge[i] = graphEdge{
			from:     e.fromNode,
			to:       e.toNode,
			streetID: internID(e.street),
			nameID:   internID(e.name),
			class:    e.class,
			oneway:   e.oneway,
		}
	}

	newEdges := make([]rawEdge, 0, len(edges))
	keep, err := simplifyGraphCore(sliceEdges(ge), nodeCoords, nodeCount, func(e graphEdge) error {
		newEdges = append(newEdges, rawEdge{
			fromNode: e.from,
			toNode:   e.to,
			street:   reverse[e.streetID],
			name:     reverse[e.nameID],
			class:    e.class,
			oneway:   e.oneway,
		})
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}

	isKeep := func(node uint32) bool {
		return keep[node>>6]&(1<<(uint(node)&63)) != 0
	}

	// Filter the point slice in place: a road point survives only when an edge
	// still references it or it has no edges at all.
	filtered := points[:0]
	newNodeOrig := make([]uint32, nodeCount)
	for i := range points {
		p := &points[i]
		if graphNode := p.graphNode; graphNode != 0 {
			if !isKeep(graphNode) {
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
