package rangeindex

import (
	"math/rand/v2"
	"sync"
	"testing"

	"golang.org/x/exp/constraints"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func collect[K constraints.Integer, V comparable](idx *Index[K, V]) []Segment[K, V] {
	return idx.CollectRanges()
}

func expectSegments[K constraints.Integer, V comparable](t *testing.T, idx *Index[K, V], want []Segment[K, V]) {
	t.Helper()
	got := collect(idx)
	if len(got) != len(want) {
		t.Fatalf("segment count: got %d, want %d\n  got:  %+v\n  want: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("segment[%d]: got %+v, want %+v\n  all got:  %+v", i, got[i], want[i], got)
		}
	}
}

func seg[K constraints.Integer, V comparable](lo, hi K, v V) Segment[K, V] {
	return Segment[K, V]{Lo: lo, Hi: hi, Value: v}
}

// setRange is a test helper that sets every key in [lo, hi) to value using
// the point-based API, to build up known range state.
func setRange[V comparable](idx *Index[int, V], lo, hi int, value V) {
	for k := lo; k < hi; k++ {
		idx.Set(k, value)
	}
}

// deleteRange is a test helper that deletes every key in [lo, hi) using
// the point-based API.
func deleteRange[V comparable](idx *Index[int, V], lo, hi int) {
	for k := lo; k < hi; k++ {
		idx.Delete(k)
	}
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

func TestNewEmpty(t *testing.T) {
	idx := New[int, string]()
	if idx.Len() != 0 {
		t.Fatalf("expected 0, got %d", idx.Len())
	}
	_, ok := idx.Get(42)
	if ok {
		t.Fatal("expected no coverage")
	}
}

func TestNewWithDegreeClamp(t *testing.T) {
	idx := NewWithDegree[int, int](0) // should clamp to 2
	if idx == nil {
		t.Fatal("nil index")
	}
}

// ---------------------------------------------------------------------------
// Set — single point
// ---------------------------------------------------------------------------

func TestSetSinglePoint(t *testing.T) {
	idx := New[int, string]()
	idx.Set(10, "A")

	// Single point → segment [10, 11)
	expectSegments(t, idx, []Segment[int, string]{seg(10, 11, "A")})
}

func TestSetSamePointTwiceSameValue(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")
	idx.Set(5, "A")

	expectSegments(t, idx, []Segment[int, string]{seg(5, 6, "A")})
}

func TestSetSamePointOverwrite(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")
	idx.Set(5, "B")

	expectSegments(t, idx, []Segment[int, string]{seg(5, 6, "B")})
}

// ---------------------------------------------------------------------------
// Automatic merging of adjacent points
// ---------------------------------------------------------------------------

func TestMergeConsecutivePointsSameValue(t *testing.T) {
	idx := New[int, string]()
	idx.Set(1, "A")
	idx.Set(2, "A")
	idx.Set(3, "A")

	// Three consecutive points with same value → merged into [1, 4)
	expectSegments(t, idx, []Segment[int, string]{seg(1, 4, "A")})
}

func TestMergeConsecutivePointsReverseOrder(t *testing.T) {
	idx := New[int, string]()
	idx.Set(3, "A")
	idx.Set(2, "A")
	idx.Set(1, "A")

	expectSegments(t, idx, []Segment[int, string]{seg(1, 4, "A")})
}

func TestMergeConsecutivePointsRandomOrder(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "X")
	idx.Set(3, "X")
	idx.Set(4, "X") // fills gap between 3 and 5

	expectSegments(t, idx, []Segment[int, string]{seg(3, 6, "X")})
}

func TestNoMergeGap(t *testing.T) {
	idx := New[int, string]()
	idx.Set(1, "A")
	idx.Set(3, "A") // gap at 2

	expectSegments(t, idx, []Segment[int, string]{
		seg(1, 2, "A"),
		seg(3, 4, "A"),
	})
}

func TestNoMergeDifferentValue(t *testing.T) {
	idx := New[int, string]()
	idx.Set(1, "A")
	idx.Set(2, "B") // adjacent but different value

	expectSegments(t, idx, []Segment[int, string]{
		seg(1, 2, "A"),
		seg(2, 3, "B"),
	})
}

func TestMergeFillsGap(t *testing.T) {
	idx := New[int, string]()
	idx.Set(1, "A")
	idx.Set(3, "A")
	// Now fill the gap
	idx.Set(2, "A")

	expectSegments(t, idx, []Segment[int, string]{seg(1, 4, "A")})
}

func TestMergeLargeConsecutiveRange(t *testing.T) {
	idx := New[int, int]()
	for i := range 100 {
		idx.Set(i, 42)
	}

	// All 100 consecutive points with same value → one segment [0, 100)
	expectSegments(t, idx, []Segment[int, int]{seg(0, 100, 42)})
}

func TestMergeNonContiguousGroups(t *testing.T) {
	idx := New[int, string]()
	// Group 1: 0,1,2
	idx.Set(0, "A")
	idx.Set(1, "A")
	idx.Set(2, "A")
	// Group 2: 10,11,12
	idx.Set(10, "B")
	idx.Set(11, "B")
	idx.Set(12, "B")

	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 3, "A"),
		seg(10, 13, "B"),
	})
}

// ---------------------------------------------------------------------------
// Overwrite splits existing ranges
// ---------------------------------------------------------------------------

func TestOverwriteMiddleOfRange(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	// Overwrite single point in the middle with different value
	idx.Set(5, "B")

	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 5, "A"),
		seg(5, 6, "B"),
		seg(6, 10, "A"),
	})
}

func TestOverwriteLeftEdgeOfRange(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	idx.Set(0, "B")

	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 1, "B"),
		seg(1, 10, "A"),
	})
}

func TestOverwriteRightEdgeOfRange(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	idx.Set(9, "B")

	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 9, "A"),
		seg(9, 10, "B"),
	})
}

func TestOverwriteWithSameValueNoSplit(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	idx.Set(5, "A") // same value, no change

	expectSegments(t, idx, []Segment[int, string]{seg(0, 10, "A")})
}

// ---------------------------------------------------------------------------
// Delete — single point
// ---------------------------------------------------------------------------

func TestDeleteSinglePoint(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")
	idx.Delete(5)

	expectSegments(t, idx, nil)
}

func TestDeleteFromMiddleOfRange(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	idx.Delete(5)

	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 5, "A"),
		seg(6, 10, "A"),
	})
}

func TestDeleteLeftEdge(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	idx.Delete(0)

	expectSegments(t, idx, []Segment[int, string]{seg(1, 10, "A")})
}

func TestDeleteRightEdge(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	idx.Delete(9)

	expectSegments(t, idx, []Segment[int, string]{seg(0, 9, "A")})
}

func TestDeleteUncovered(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")
	idx.Delete(99) // nothing there

	expectSegments(t, idx, []Segment[int, string]{seg(5, 6, "A")})
}

func TestDeleteMultiplePointsSplitsRange(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 20, "A")
	// Delete 5..9 (five points)
	for k := 5; k < 10; k++ {
		idx.Delete(k)
	}

	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 5, "A"),
		seg(10, 20, "A"),
	})
}

func TestDeleteEntireRange(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 5, "A")
	for k := 0; k < 5; k++ {
		idx.Delete(k)
	}

	expectSegments(t, idx, nil)
}

// ---------------------------------------------------------------------------
// Get / Contains
// ---------------------------------------------------------------------------

func TestGetInside(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 10, 20, "A")

	for _, k := range []int{10, 14, 19} {
		v, ok := idx.Get(k)
		if !ok || v != "A" {
			t.Fatalf("Get(%d): expected ('A', true), got (%q, %v)", k, v, ok)
		}
	}
}

func TestGetOutside(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 10, 20, "A")

	for _, k := range []int{9, 20, 100, -5} {
		_, ok := idx.Get(k)
		if ok {
			t.Fatalf("Get(%d): expected not found", k)
		}
	}
}

func TestGetAfterMultipleSegments(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 5, "A")
	setRange(idx, 10, 15, "B")
	setRange(idx, 20, 25, "C")

	tests := []struct {
		key  int
		want string
		ok   bool
	}{
		{0, "A", true},
		{4, "A", true},
		{5, "", false},
		{7, "", false},
		{10, "B", true},
		{14, "B", true},
		{15, "", false},
		{20, "C", true},
		{24, "C", true},
		{25, "", false},
	}
	for _, tc := range tests {
		v, ok := idx.Get(tc.key)
		if ok != tc.ok || v != tc.want {
			t.Errorf("Get(%d): got (%q, %v), want (%q, %v)", tc.key, v, ok, tc.want, tc.ok)
		}
	}
}

func TestContains(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 5, 10, "X")

	if !idx.Contains(7) {
		t.Fatal("expected 7 to be covered")
	}
	if idx.Contains(10) {
		t.Fatal("expected 10 NOT covered (half-open)")
	}
}

// ---------------------------------------------------------------------------
// SetIfAbsent
// ---------------------------------------------------------------------------

func TestSetIfAbsentEmpty(t *testing.T) {
	idx := New[int, string]()
	ok := idx.SetIfAbsent(5, "A")
	if !ok {
		t.Fatal("expected true for empty index")
	}
	expectSegments(t, idx, []Segment[int, string]{seg(5, 6, "A")})
}

func TestSetIfAbsentNoOverlap(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")

	ok := idx.SetIfAbsent(10, "B")
	if !ok {
		t.Fatal("expected true, keys don't overlap")
	}
	expectSegments(t, idx, []Segment[int, string]{
		seg(5, 6, "A"),
		seg(10, 11, "B"),
	})
}

func TestSetIfAbsentOverlap(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")

	ok := idx.SetIfAbsent(5, "B")
	if ok {
		t.Fatal("expected false, key already covered")
	}
	// Nothing changed
	expectSegments(t, idx, []Segment[int, string]{seg(5, 6, "A")})
}

func TestSetIfAbsentCoveredByRange(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")

	ok := idx.SetIfAbsent(5, "B")
	if ok {
		t.Fatal("expected false, key is inside existing range")
	}
	expectSegments(t, idx, []Segment[int, string]{seg(0, 10, "A")})
}

func TestSetIfAbsentAdjacentMerges(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")

	ok := idx.SetIfAbsent(6, "A") // adjacent, same value
	if !ok {
		t.Fatal("expected true, no overlap")
	}
	// Should merge into one segment
	expectSegments(t, idx, []Segment[int, string]{seg(5, 7, "A")})
}

func TestSetIfAbsentAdjacentDifferentValue(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")

	ok := idx.SetIfAbsent(6, "B") // adjacent, different value
	if !ok {
		t.Fatal("expected true, no overlap")
	}
	expectSegments(t, idx, []Segment[int, string]{
		seg(5, 6, "A"),
		seg(6, 7, "B"),
	})
}

// ---------------------------------------------------------------------------
// Ranges iteration
// ---------------------------------------------------------------------------

func TestRanges(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 10, 20, "A")
	setRange(idx, 30, 40, "B")

	var result []Segment[int, string]
	idx.Ranges(func(lo, hi int, v string) bool {
		result = append(result, seg(lo, hi, v))
		return true
	})

	want := []Segment[int, string]{
		seg(10, 20, "A"),
		seg(30, 40, "B"),
	}
	if len(result) != len(want) {
		t.Fatalf("got %d, want %d", len(result), len(want))
	}
	for i := range want {
		if result[i] != want[i] {
			t.Fatalf("[%d]: got %+v, want %+v", i, result[i], want[i])
		}
	}
}

func TestRangesEarlyStop(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 5, "A")
	setRange(idx, 10, 15, "B")
	setRange(idx, 20, 25, "C")

	var count int
	idx.Ranges(func(lo, hi int, v string) bool {
		count++
		return false // stop immediately
	})
	if count != 1 {
		t.Fatalf("expected 1, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Clear / Len
// ---------------------------------------------------------------------------

func TestClear(t *testing.T) {
	idx := New[int, int]()
	setRange(idx, 0, 10, 1)
	setRange(idx, 20, 30, 2)
	idx.Clear()

	if idx.Len() != 0 {
		t.Fatalf("expected 0, got %d", idx.Len())
	}
	if idx.Contains(5) {
		t.Fatal("expected no coverage after Clear")
	}
}

func TestLenCountsSegments(t *testing.T) {
	idx := New[int, string]()
	if idx.Len() != 0 {
		t.Fatalf("expected 0, got %d", idx.Len())
	}
	setRange(idx, 0, 10, "A")
	setRange(idx, 20, 30, "B")
	if idx.Len() != 2 {
		t.Fatalf("expected 2, got %d", idx.Len())
	}
	// Fill gap with "A" → merges [0,10)=A and [10,20)=A but not [20,30)=B
	setRange(idx, 10, 20, "A")
	if idx.Len() != 2 {
		t.Fatalf("expected 2 after partial merge, got %d", idx.Len())
	}
}

// ---------------------------------------------------------------------------
// Complex scenarios
// ---------------------------------------------------------------------------

func TestPaintOverScenario(t *testing.T) {
	idx := New[int, string]()

	// Paint the full range red
	setRange(idx, 0, 100, "red")
	// Paint a stripe green
	setRange(idx, 20, 40, "green")
	// Paint another stripe blue
	setRange(idx, 60, 80, "blue")

	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 20, "red"),
		seg(20, 40, "green"),
		seg(40, 60, "red"),
		seg(60, 80, "blue"),
		seg(80, 100, "red"),
	})

	// Paint over green+gap+blue with yellow
	setRange(idx, 20, 80, "yellow")
	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 20, "red"),
		seg(20, 80, "yellow"),
		seg(80, 100, "red"),
	})

	// Paint everything red again → should merge into one
	setRange(idx, 0, 100, "red")
	expectSegments(t, idx, []Segment[int, string]{seg(0, 100, "red")})
}

func TestRepeatedInsertDeleteCycle(t *testing.T) {
	idx := New[int, int]()

	setRange(idx, 0, 100, 1)
	deleteRange(idx, 30, 70)
	expectSegments(t, idx, []Segment[int, int]{
		seg(0, 30, 1),
		seg(70, 100, 1),
	})

	setRange(idx, 30, 70, 1) // fill the gap → merges
	expectSegments(t, idx, []Segment[int, int]{seg(0, 100, 1)})

	setRange(idx, 50, 60, 2)
	setRange(idx, 40, 50, 2) // should merge with [50,60)=2
	expectSegments(t, idx, []Segment[int, int]{
		seg(0, 40, 1),
		seg(40, 60, 2),
		seg(60, 100, 1),
	})

	deleteRange(idx, 0, 100)
	expectSegments(t, idx, nil)
}

func TestSeenSetPattern(t *testing.T) {
	// This mirrors the actual usage in geoparser: marking IDs as "seen"
	idx := New[int64, struct{}]()
	empty := struct{}{}

	// Mark some IDs as seen
	ok := idx.SetIfAbsent(100, empty)
	if !ok {
		t.Fatal("expected true")
	}
	ok = idx.SetIfAbsent(101, empty)
	if !ok {
		t.Fatal("expected true")
	}
	ok = idx.SetIfAbsent(102, empty)
	if !ok {
		t.Fatal("expected true")
	}

	// Should have merged into [100, 103)
	if idx.Len() != 1 {
		t.Fatalf("expected 1 merged segment, got %d", idx.Len())
	}

	// Duplicate should be rejected
	ok = idx.SetIfAbsent(101, empty)
	if ok {
		t.Fatal("expected false for duplicate")
	}

	// Non-adjacent should create new segment
	ok = idx.SetIfAbsent(200, empty)
	if !ok {
		t.Fatal("expected true for non-adjacent")
	}
	if idx.Len() != 2 {
		t.Fatalf("expected 2 segments, got %d", idx.Len())
	}
}

// ---------------------------------------------------------------------------
// Large data set
// ---------------------------------------------------------------------------

func TestLargeDataSet(t *testing.T) {
	idx := New[int, int]()
	const n = 1000

	// Insert n non-overlapping single points with gaps
	for i := range n {
		idx.Set(i*10, i)
	}
	if idx.Len() != n {
		t.Fatalf("expected %d segments (no merging, gaps), got %d", n, idx.Len())
	}

	// Verify all point lookups
	for i := range n {
		v, ok := idx.Get(i * 10)
		if !ok || v != i {
			t.Fatalf("Get(%d): expected %d, got %d (ok=%v)", i*10, i, v, ok)
		}
		// Point between segments
		_, ok = idx.Get(i*10 + 5)
		if ok {
			t.Fatalf("Get(%d): expected no coverage", i*10+5)
		}
	}

	// Paint over everything with a single value using consecutive keys
	for k := 0; k < n*10; k++ {
		idx.Set(k, 999)
	}
	if idx.Len() != 1 {
		t.Fatalf("expected 1 after full overwrite, got %d", idx.Len())
	}
	v, ok := idx.Get(50)
	if !ok || v != 999 {
		t.Fatalf("expected 999, got %d", v)
	}
}

func TestLargeConsecutiveMerge(t *testing.T) {
	idx := New[int, int]()

	// Insert 10000 consecutive points — should end up as 1 segment
	const n = 10000
	for i := range n {
		idx.Set(i, 1)
	}
	if idx.Len() != 1 {
		t.Fatalf("expected 1 merged segment, got %d", idx.Len())
	}
	expectSegments(t, idx, []Segment[int, int]{seg(0, n, 1)})
}

// ---------------------------------------------------------------------------
// Integer type variations
// ---------------------------------------------------------------------------

func TestInt8Keys(t *testing.T) {
	idx := New[int8, string]()
	idx.Set(1, "A")
	idx.Set(2, "A")
	idx.Set(3, "A")

	expectSegments(t, idx, []Segment[int8, string]{seg[int8](1, 4, "A")})

	v, ok := idx.Get(2)
	if !ok || v != "A" {
		t.Fatalf("expected A, got %q", v)
	}
}

func TestUint64Keys(t *testing.T) {
	idx := New[uint64, int]()
	idx.Set(1000000, 1)
	idx.Set(1000001, 1)
	idx.Set(1000002, 1)

	expectSegments(t, idx, []Segment[uint64, int]{seg[uint64](1000000, 1000003, 1)})
}

func TestInt64Keys(t *testing.T) {
	// Mirrors actual usage with osm.NodeID (int64)
	idx := New[int64, struct{}]()
	empty := struct{}{}

	idx.Set(42, empty)
	idx.Set(43, empty)

	expectSegments(t, idx, []Segment[int64, struct{}]{seg[int64](42, 44, empty)})
	if !idx.Contains(42) {
		t.Fatal("expected 42 covered")
	}
	if !idx.Contains(43) {
		t.Fatal("expected 43 covered")
	}
	if idx.Contains(44) {
		t.Fatal("expected 44 NOT covered")
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestDeleteAndReinsert(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	idx.Delete(5)

	// Now reinsert with same value → should re-merge
	idx.Set(5, "A")
	expectSegments(t, idx, []Segment[int, string]{seg(0, 10, "A")})
}

func TestDeleteAndReinsertDifferentValue(t *testing.T) {
	idx := New[int, string]()
	setRange(idx, 0, 10, "A")
	idx.Delete(5)
	idx.Set(5, "B")

	expectSegments(t, idx, []Segment[int, string]{
		seg(0, 5, "A"),
		seg(5, 6, "B"),
		seg(6, 10, "A"),
	})
}

func TestSetNegativeKeys(t *testing.T) {
	idx := New[int, string]()
	idx.Set(-3, "A")
	idx.Set(-2, "A")
	idx.Set(-1, "A")

	expectSegments(t, idx, []Segment[int, string]{seg(-3, 0, "A")})
}

func TestSetZero(t *testing.T) {
	idx := New[int, string]()
	idx.Set(0, "Z")

	v, ok := idx.Get(0)
	if !ok || v != "Z" {
		t.Fatalf("expected Z, got %q (ok=%v)", v, ok)
	}
}

func TestCollectRangesEmpty(t *testing.T) {
	idx := New[int, string]()
	segs := idx.CollectRanges()
	if len(segs) != 0 {
		t.Fatalf("expected empty, got %+v", segs)
	}
}

func TestContainsAfterDelete(t *testing.T) {
	idx := New[int, string]()
	idx.Set(5, "A")
	if !idx.Contains(5) {
		t.Fatal("expected covered")
	}
	idx.Delete(5)
	if idx.Contains(5) {
		t.Fatal("expected NOT covered after delete")
	}
}

// ---------------------------------------------------------------------------
// Concurrency tests
// ---------------------------------------------------------------------------

func TestConcurrentReads(t *testing.T) {
	idx := New[int, int]()
	setRange(idx, 0, 1000, 42)

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 1000 {
				v, ok := idx.Get(i)
				if !ok || v != 42 {
					t.Errorf("Get(%d): expected 42, got %d (ok=%v)", i, v, ok)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentWrites(t *testing.T) {
	idx := New[int, int]()

	var wg sync.WaitGroup
	const goroutines = 10
	const perG = 100

	for g := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			base := id * perG * 10 // non-overlapping ranges per goroutine
			for i := range perG {
				idx.Set(base+i*10, id)
			}
		}(g)
	}
	wg.Wait()

	// Every point should be queryable
	for g := range goroutines {
		base := g * perG * 10
		for i := range perG {
			key := base + i*10
			v, ok := idx.Get(key)
			if !ok {
				t.Fatalf("Get(%d): expected coverage", key)
			}
			if v != g {
				t.Fatalf("Get(%d): expected %d, got %d", key, g, v)
			}
		}
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	idx := New[int, int]()
	setRange(idx, 0, 10000, 0)

	var wg sync.WaitGroup

	// Writers
	for w := range 4 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 500 {
				key := rand.IntN(10000)
				idx.Set(key, id*1000+i)
			}
		}(w)
	}

	// Readers
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2000 {
				idx.Get(rand.IntN(10000))
				idx.Contains(rand.IntN(10000))
				idx.Len()
			}
		}()
	}

	wg.Wait()
	// No race/panic is the success condition.
}

func TestConcurrentSetDelete(t *testing.T) {
	idx := New[int, int]()

	var wg sync.WaitGroup

	// Inserters
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 500 {
			idx.Set(i, i)
		}
	}()

	// Deleters
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 500 {
			idx.Delete(i)
		}
	}()

	// Readers
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 2000 {
			idx.Get(rand.IntN(500))
			idx.Len()
		}
	}()

	wg.Wait()
}

func TestConcurrentSetIfAbsent(t *testing.T) {
	idx := New[int, int]()
	const goroutines = 100

	var wg sync.WaitGroup
	wins := make(chan int, goroutines)

	for g := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if idx.SetIfAbsent(42, id) {
				wins <- id
			}
		}(g)
	}
	wg.Wait()
	close(wins)

	var winCount int
	var winner int
	for id := range wins {
		winCount++
		winner = id
	}
	if winCount != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", winCount)
	}

	v, ok := idx.Get(42)
	if !ok || v != winner {
		t.Fatalf("expected %d, got %d (ok=%v)", winner, v, ok)
	}
}

func TestConcurrentMixedOperations(t *testing.T) {
	idx := New[int, int]()

	var wg sync.WaitGroup

	// SetIfAbsent workers
	for g := range 4 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 200 {
				idx.SetIfAbsent(i, id)
			}
		}(g)
	}

	// Set workers
	for g := range 4 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 200 {
				idx.Set(i+200, id)
			}
		}(g)
	}

	// Reader workers
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 500 {
				idx.Get(rand.IntN(400))
				idx.Contains(rand.IntN(400))
				idx.CollectRanges()
			}
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkSet(b *testing.B) {
	idx := New[int, int]()
	b.ResetTimer()
	for i := range b.N {
		idx.Set(i, i)
	}
}

func BenchmarkSetSparse(b *testing.B) {
	idx := New[int, int]()
	b.ResetTimer()
	for i := range b.N {
		idx.Set(i*10, i) // gaps prevent merging
	}
}

func BenchmarkGet(b *testing.B) {
	idx := New[int, int]()
	const n = 10000
	for i := range n {
		idx.Set(i, i)
	}
	b.ResetTimer()
	for range b.N {
		idx.Get(rand.IntN(n))
	}
}

func BenchmarkSetIfAbsent(b *testing.B) {
	idx := New[int, int]()
	b.ResetTimer()
	for i := range b.N {
		idx.SetIfAbsent(i, i)
	}
}

func BenchmarkDelete(b *testing.B) {
	idx := New[int, int]()
	for i := range b.N {
		idx.Set(i, i)
	}
	b.ResetTimer()
	for i := range b.N {
		idx.Delete(i)
	}
}

func BenchmarkConcurrentReadWrite(b *testing.B) {
	idx := New[int, int]()
	for i := range 100000 {
		idx.Set(i, 0)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%4 == 0 {
				idx.Set(rand.IntN(100000), i)
			} else {
				idx.Get(rand.IntN(100000))
			}
			i++
		}
	})
}
