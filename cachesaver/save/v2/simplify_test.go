package savev2

import (
	"math"
	"testing"
)

func testPoint(graphNode uint32, x, y float64) rawPoint {
	return rawPoint{x: x, y: y, graphNode: graphNode}
}

func testEdge(from, to uint32, street string, class, oneway uint8) rawEdge {
	return rawEdge{fromNode: from, toNode: to, street: street, class: class, oneway: oneway}
}

func nodeSet(points []rawPoint) map[uint32]bool {
	out := make(map[uint32]bool, len(points))
	for _, p := range points {
		if p.graphNode != 0 {
			out[p.graphNode] = true
		}
	}
	return out
}

func edgeMeters(points []rawPoint, e rawEdge) float64 {
	index := make(map[uint32]rawPoint, len(points))
	for _, p := range points {
		index[p.graphNode] = p
	}
	a, b := index[e.fromNode], index[e.toNode]
	scale := math.Cos((a.y + b.y) * 0.5 * math.Pi / 180)
	dx := (b.x - a.x) * metersPerDegreeLat * scale
	dy := (b.y - a.y) * metersPerDegreeLat
	return math.Hypot(dx, dy)
}

func TestSimplifyCollinearChainDropsInteriorNodes(t *testing.T) {
	points := []rawPoint{
		testPoint(1, 0.0, 0.0),
		testPoint(2, 0.0001, 0.0),
		testPoint(3, 0.0002, 0.0),
		testPoint(4, 0.0003, 0.0),
	}
	edges := []rawEdge{
		testEdge(1, 2, "Main Street", 3, 0),
		testEdge(2, 3, "Main Street", 3, 0),
		testEdge(3, 4, "Main Street", 3, 0),
	}

	filtered, newEdges, nodeOrig, err := simplifyGraph(points, edges, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEdges) != 1 {
		t.Fatalf("expected a single merged edge, got %d", len(newEdges))
	}
	if newEdges[0].fromNode != 1 || newEdges[0].toNode != 4 {
		t.Fatalf("expected edge 1 -> 4, got %d -> %d", newEdges[0].fromNode, newEdges[0].toNode)
	}
	if got := nodeSet(filtered); len(got) != 2 || !got[1] || !got[4] {
		t.Fatalf("expected only nodes 1 and 4 to remain, got %v", got)
	}
	// nodeOrig maps the surviving node ids to filtered indexes.
	for node, index := range map[uint32]uint32{1: 0, 4: 1} {
		if int(nodeOrig[node]) != int(index) {
			t.Fatalf("node %d: expected filtered index %d, got %d", node, index, nodeOrig[node])
		}
	}
}

func TestSimplifyKeepsCurves(t *testing.T) {
	points := []rawPoint{
		testPoint(1, 0.0, 0.0),
		testPoint(2, 0.001, 0.0),
		testPoint(3, 0.001, 0.001),
	}
	edges := []rawEdge{
		testEdge(1, 2, "A", 3, 0),
		testEdge(2, 3, "A", 3, 0),
	}

	_, newEdges, _, err := simplifyGraph(points, edges, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEdges) != 2 {
		t.Fatalf("expected the corner to be kept (2 edges), got %d", len(newEdges))
	}
}

func TestSimplifyDropsOnlySubEpsilonDeviations(t *testing.T) {
	// The chain (111 m end to end) stays under the edge cap, so only the
	// tolerance decides.
	makeChain := func(offset float64) []rawPoint {
		return []rawPoint{
			testPoint(1, 0.0, 0.0),
			testPoint(2, 0.0005, offset),
			testPoint(3, 0.001, 0.0),
		}
	}
	edges := []rawEdge{testEdge(1, 2, "A", 3, 0), testEdge(2, 3, "A", 3, 0)}

	_, within, _, err := simplifyGraph(makeChain(graphSimplifyEpsilon*0.5), edges, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(within) != 1 {
		t.Fatalf("deviation below epsilon must be dropped, got %d edges", len(within))
	}

	_, beyond, _, err := simplifyGraph(makeChain(graphSimplifyEpsilon*10), edges, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(beyond) != 2 {
		t.Fatalf("deviation above epsilon must be kept, got %d edges", len(beyond))
	}
}

func TestSimplifyEdgeCapKeepsNodes(t *testing.T) {
	// Eleven collinear nodes ~55.7 m apart: the cap forces intermediate
	// vertices to survive even though the line is perfectly straight.
	points := make([]rawPoint, 0, 11)
	for i := 0; i < 11; i++ {
		points = append(points, testPoint(uint32(i+1), float64(i)*0.0005, 0.0))
	}
	edges := make([]rawEdge, 0, 10)
	for i := 1; i <= 10; i++ {
		edges = append(edges, testEdge(uint32(i), uint32(i+1), "A", 3, 0))
	}

	filtered, newEdges, _, err := simplifyGraph(points, edges, 11)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEdges) >= 10 || len(newEdges) < 4 {
		t.Fatalf("expected the straight chain to be simplified but capped, got %d edges", len(newEdges))
	}
	for _, e := range newEdges {
		if m := edgeMeters(points, e); m > graphMaxEdgeMeters+0.1 {
			t.Fatalf("edge %d -> %d is %.1f m, above the cap", e.fromNode, e.toNode, m)
		}
	}
	if len(filtered) != len(nodeSet(filtered)) {
		t.Fatalf("non-road points must not appear in a road-only test")
	}
}

func TestSimplifyPreservesJunctions(t *testing.T) {
	// A T junction on an otherwise straight road: node 2 has degree 3.
	points := []rawPoint{
		testPoint(1, 0.0, 0.0),
		testPoint(2, 0.0001, 0.0),
		testPoint(3, 0.0002, 0.0),
		testPoint(4, 0.0001, 0.0001),
	}
	edges := []rawEdge{
		testEdge(1, 2, "A", 3, 0),
		testEdge(2, 3, "A", 3, 0),
		testEdge(2, 4, "A", 3, 0),
	}

	filtered, newEdges, _, err := simplifyGraph(points, edges, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEdges) != 3 {
		t.Fatalf("junction chains must not merge across the junction, got %d edges", len(newEdges))
	}
	for _, e := range newEdges {
		if e.fromNode == 2 || e.toNode == 2 {
			continue
		}
		t.Fatalf("edge %d -> %d bypasses the junction", e.fromNode, e.toNode)
	}
	if got := nodeSet(filtered); !got[2] {
		t.Fatal("junction node must be kept")
	}
}

func TestSimplifyPreservesPropertyBoundaries(t *testing.T) {
	points := []rawPoint{
		testPoint(1, 0.0, 0.0),
		testPoint(2, 0.0001, 0.0),
		testPoint(3, 0.0002, 0.0),
	}
	edges := []rawEdge{
		testEdge(1, 2, "A", 3, 0),
		testEdge(2, 3, "A", 4, 0), // class changes
	}

	_, newEdges, _, err := simplifyGraph(points, edges, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEdges) != 2 {
		t.Fatalf("property boundary must break the chain, got %d edges", len(newEdges))
	}
}

func TestSimplifyPreservesDirectedOrientation(t *testing.T) {
	points := []rawPoint{
		testPoint(1, 0.0, 0.0),
		testPoint(2, 0.0001, 0.0),
		testPoint(3, 0.0002, 0.0),
	}
	forward := []rawEdge{
		testEdge(1, 2, "A", 3, 1),
		testEdge(2, 3, "A", 3, 1),
	}
	reversed := []rawEdge{
		testEdge(2, 3, "A", 3, 1),
		testEdge(1, 2, "A", 3, 1),
	}

	for name, edges := range map[string][]rawEdge{"forward": forward, "reversed": reversed} {
		_, newEdges, _, err := simplifyGraph(points, edges, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(newEdges) != 1 {
			t.Fatalf("%s: expected one merged edge, got %d", name, len(newEdges))
		}
		if newEdges[0].fromNode != 1 || newEdges[0].toNode != 3 {
			t.Fatalf("%s: directed orientation lost: %d -> %d",
				name, newEdges[0].fromNode, newEdges[0].toNode)
		}
		if newEdges[0].oneway != 1 {
			t.Fatalf("%s: oneway property lost: %d", name, newEdges[0].oneway)
		}
	}
}

func TestSimplifyClosedLoop(t *testing.T) {
	// A rectangle (111 m per side, under the edge cap) with one collinear
	// midpoint per side.
	coords := []struct{ x, y float64 }{
		{0.0, 0.0}, {0.0005, 0.0}, {0.001, 0.0}, {0.001, 0.0005},
		{0.001, 0.001}, {0.0005, 0.001}, {0.0, 0.001}, {0.0, 0.0005},
	}
	points := make([]rawPoint, 0, 8)
	edges := make([]rawEdge, 0, 8)
	for i := 0; i < 8; i++ {
		points = append(points, testPoint(uint32(i+1), coords[i].x, coords[i].y))
		j := (i + 1) % 8
		edges = append(edges, testEdge(uint32(i+1), uint32(j+1), "A", 3, 0))
	}

	_, newEdges, _, err := simplifyGraph(points, edges, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEdges) != 4 {
		t.Fatalf("expected the four corners, got %d edges", len(newEdges))
	}
	// The loop must close: the last edge ends where the first starts.
	if newEdges[len(newEdges)-1].toNode != newEdges[0].fromNode {
		t.Fatalf("loop is not closed: %d -> ... -> %d",
			newEdges[0].fromNode, newEdges[len(newEdges)-1].toNode)
	}
}

func TestSimplifyKeepsIsolatedAndNonRoadPoints(t *testing.T) {
	points := []rawPoint{
		testPoint(1, 0.0, 0.0),
		testPoint(2, 0.0001, 0.0),
		testPoint(3, 0.0002, 0.0),
		testPoint(0, 10.0, 10.0), // addressed building (non-road)
		testPoint(9, 20.0, 20.0), // isolated road node, no edges
	}
	edges := []rawEdge{
		testEdge(1, 2, "A", 3, 0),
		testEdge(2, 3, "A", 3, 0),
	}

	filtered, newEdges, _, err := simplifyGraph(points, edges, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEdges) != 1 {
		t.Fatalf("expected one merged edge, got %d", len(newEdges))
	}
	if len(filtered) != 4 {
		t.Fatalf("expected 4 surviving points, got %d", len(filtered))
	}
	got := map[uint32]bool{}
	for _, p := range filtered {
		got[p.graphNode] = true
	}
	if !got[1] || !got[3] || !got[0] || !got[9] || got[2] {
		t.Fatalf("unexpected surviving points: %v", got)
	}
}

func TestSimplifyErrorsOnMissingNode(t *testing.T) {
	points := []rawPoint{testPoint(1, 0.0, 0.0)}
	edges := []rawEdge{testEdge(1, 5, "A", 3, 0)}

	if _, _, _, err := simplifyGraph(points, edges, 4); err == nil {
		t.Fatal("expected an error for a node without an emitted point")
	}
}

func TestSimplifyAllowsRawGapsAboveTheCap(t *testing.T) {
	// A raw OSM segment longer than 150 m cannot be split and must survive.
	points := []rawPoint{
		testPoint(1, 0.0, 0.0),
		testPoint(2, 0.01, 0.0), // ~1.1 km
	}
	edges := []rawEdge{testEdge(1, 2, "A", 3, 0)}

	_, newEdges, _, err := simplifyGraph(points, edges, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEdges) != 1 {
		t.Fatalf("expected the raw edge to survive, got %d edges", len(newEdges))
	}
}
