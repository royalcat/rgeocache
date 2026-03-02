// Package rangeindex provides a thread-safe, dynamically updatable in-memory
// index that stores sets of discrete keys, each associated with a value.
// Internally, adjacent keys with equal values are automatically merged into
// half-open ranges [Lo, Hi) to minimise memory use.
//
// The public API is point-based: callers set, delete, and query individual
// keys. The range creation, merging, and splitting happens transparently
// inside the index.
//
// It is backed by a B-tree (github.com/google/btree) and protected by a
// sync.RWMutex so that concurrent reads and writes are safe.
//
// K is constrained to integer types so that the index can compute key+1 for
// half-open interval arithmetic.
package rangeindex

import (
	"cmp"
	"sync"

	"github.com/google/btree"
	"golang.org/x/exp/constraints"
)

const defaultDegree = 16

// segment is an internal half-open interval [Lo, Hi) mapped to Value.
type segment[K constraints.Integer, V comparable] struct {
	Lo    K
	Hi    K
	Value V
}

// Index is a thread-safe point-to-range index.
//
// Callers operate on individual keys; internally the index stores
// non-overlapping half-open intervals [Lo, Hi) and automatically merges
// adjacent entries that carry the same value.
//
// K must be an integer type (int, int64, uint32, …).
// V must be comparable (required for merge-on-equal-value).
type Index[K constraints.Integer, V comparable] struct {
	mu   sync.RWMutex
	tree *btree.BTreeG[segment[K, V]]
}

// New creates a new empty Index with the default B-tree degree.
func New[K constraints.Integer, V comparable]() *Index[K, V] {
	return NewWithDegree[K, V](defaultDegree)
}

// NewWithDegree creates a new empty Index with the given B-tree degree.
// Degree must be ≥ 2.
func NewWithDegree[K constraints.Integer, V comparable](degree int) *Index[K, V] {
	if degree < 2 {
		degree = 2
	}
	return &Index[K, V]{
		tree: btree.NewG[segment[K, V]](degree, func(a, b segment[K, V]) bool {
			return cmp.Less(a.Lo, b.Lo)
		}),
	}
}

// ---------------------------------------------------------------------------
// Internal helpers (caller must hold appropriate lock)
// ---------------------------------------------------------------------------

// findCovering returns the segment that covers key, if any.
func (idx *Index[K, V]) findCovering(key K) (segment[K, V], bool) {
	var result segment[K, V]
	var found bool

	idx.tree.DescendLessOrEqual(segment[K, V]{Lo: key}, func(seg segment[K, V]) bool {
		if key < seg.Hi {
			result = seg
			found = true
		}
		return false
	})

	return result, found
}

// findOverlapping collects every stored segment that overlaps the half-open
// interval [lo, hi). The result is in ascending Lo order.
func (idx *Index[K, V]) findOverlapping(lo, hi K) []segment[K, V] {
	var out []segment[K, V]

	startLo := lo
	idx.tree.DescendLessOrEqual(segment[K, V]{Lo: lo}, func(seg segment[K, V]) bool {
		if seg.Hi > lo {
			out = append(out, seg)
			startLo = seg.Lo
		}
		return false
	})

	idx.tree.AscendGreaterOrEqual(segment[K, V]{Lo: startLo}, func(seg segment[K, V]) bool {
		if seg.Lo >= hi {
			return false
		}
		if len(out) > 0 && seg.Lo == out[0].Lo {
			// already collected from the descend step
		} else {
			out = append(out, seg)
		}
		return true
	})

	return out
}

// mergeNeighbors tries to merge the segment at the given position with its
// left and right neighbors if they are adjacent and carry the same value.
func (idx *Index[K, V]) mergeNeighbors(seg segment[K, V]) segment[K, V] {
	// Merge with left neighbor.
	idx.tree.DescendLessOrEqual(segment[K, V]{Lo: seg.Lo}, func(left segment[K, V]) bool {
		if left.Lo == seg.Lo {
			return true // skip self
		}
		if left.Hi == seg.Lo && left.Value == seg.Value {
			idx.tree.Delete(left)
			idx.tree.Delete(seg)
			seg.Lo = left.Lo
			idx.tree.ReplaceOrInsert(seg)
		}
		return false
	})

	// Merge with right neighbor.
	idx.tree.AscendGreaterOrEqual(segment[K, V]{Lo: seg.Hi}, func(right segment[K, V]) bool {
		if right.Lo == seg.Hi && right.Value == seg.Value {
			idx.tree.Delete(right)
			idx.tree.Delete(seg)
			seg.Hi = right.Hi
			idx.tree.ReplaceOrInsert(seg)
		}
		return false
	})

	return seg
}

// internalSet assigns value to the half-open interval [lo, hi).
// Caller must hold the write lock.
func (idx *Index[K, V]) internalSet(lo, hi K, value V) {
	if lo >= hi {
		return
	}

	overlapping := idx.findOverlapping(lo, hi)

	var leftTrim, rightTrim *segment[K, V]

	if len(overlapping) > 0 {
		first := overlapping[0]
		if first.Lo < lo && first.Value != value {
			lt := segment[K, V]{Lo: first.Lo, Hi: lo, Value: first.Value}
			leftTrim = &lt
		} else if first.Lo < lo && first.Value == value {
			lo = first.Lo
		}

		last := overlapping[len(overlapping)-1]
		if last.Hi > hi && last.Value != value {
			rt := segment[K, V]{Lo: hi, Hi: last.Hi, Value: last.Value}
			rightTrim = &rt
		} else if last.Hi > hi && last.Value == value {
			hi = last.Hi
		}
	}

	for _, seg := range overlapping {
		idx.tree.Delete(seg)
	}

	if leftTrim != nil {
		idx.tree.ReplaceOrInsert(*leftTrim)
	}
	if rightTrim != nil {
		idx.tree.ReplaceOrInsert(*rightTrim)
	}

	newSeg := segment[K, V]{Lo: lo, Hi: hi, Value: value}
	idx.tree.ReplaceOrInsert(newSeg)
	idx.mergeNeighbors(newSeg)
}

// internalDelete removes coverage for the half-open interval [lo, hi).
// Caller must hold the write lock.
func (idx *Index[K, V]) internalDelete(lo, hi K) {
	if lo >= hi {
		return
	}

	overlapping := idx.findOverlapping(lo, hi)
	if len(overlapping) == 0 {
		return
	}

	var leftTrim, rightTrim *segment[K, V]

	first := overlapping[0]
	if first.Lo < lo {
		lt := segment[K, V]{Lo: first.Lo, Hi: lo, Value: first.Value}
		leftTrim = &lt
	}

	last := overlapping[len(overlapping)-1]
	if last.Hi > hi {
		rt := segment[K, V]{Lo: hi, Hi: last.Hi, Value: last.Value}
		rightTrim = &rt
	}

	for _, seg := range overlapping {
		idx.tree.Delete(seg)
	}

	if leftTrim != nil {
		idx.tree.ReplaceOrInsert(*leftTrim)
	}
	if rightTrim != nil {
		idx.tree.ReplaceOrInsert(*rightTrim)
	}
}

// ---------------------------------------------------------------------------
// Public API — mutators
// ---------------------------------------------------------------------------

// Set assigns value to the given key. If adjacent keys carry the same value,
// the entries are merged into a single internal range automatically.
func (idx *Index[K, V]) Set(key K, value V) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.internalSet(key, key+1, value)
}

// Delete removes coverage for the given key. Internal ranges are split or
// trimmed as needed.
func (idx *Index[K, V]) Delete(key K) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	idx.internalDelete(key, key+1)
}

// SetIfAbsent assigns value to the given key only if it is not already
// covered by any segment. Returns true if the key was set, false if it was
// already present.
//
// The check and insertion are atomic with respect to other Index operations.
func (idx *Index[K, V]) SetIfAbsent(key K, value V) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if _, found := idx.findCovering(key); found {
		return false
	}

	newSeg := segment[K, V]{Lo: key, Hi: key + 1, Value: value}
	idx.tree.ReplaceOrInsert(newSeg)
	idx.mergeNeighbors(newSeg)
	return true
}

// Clear removes all segments.
func (idx *Index[K, V]) Clear() {
	idx.mu.Lock()
	idx.tree.Clear(true)
	idx.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Public API — readers
// ---------------------------------------------------------------------------

// Get returns the value covering key, or the zero value and false if key is
// not covered by any segment.
func (idx *Index[K, V]) Get(key K) (V, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	seg, found := idx.findCovering(key)
	if found {
		return seg.Value, true
	}
	var zv V
	return zv, false
}

// Contains returns true if key is covered by some segment.
func (idx *Index[K, V]) Contains(key K) bool {
	_, ok := idx.Get(key)
	return ok
}

// Len returns the number of stored internal segments (not the number of
// covered keys). This is useful for observing how well merging is working.
func (idx *Index[K, V]) Len() int {
	idx.mu.RLock()
	n := idx.tree.Len()
	idx.mu.RUnlock()
	return n
}

// Segment is a read-only view of a stored range returned by iteration
// methods.
type Segment[K constraints.Integer, V comparable] struct {
	Lo    K
	Hi    K
	Value V
}

// Ranges iterates over all stored segments in ascending Lo order.
// Iteration stops early if fn returns false.
func (idx *Index[K, V]) Ranges(fn func(lo, hi K, value V) bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	idx.tree.Ascend(func(seg segment[K, V]) bool {
		return fn(seg.Lo, seg.Hi, seg.Value)
	})
}

// CollectRanges returns a snapshot of all stored segments in ascending Lo
// order.
func (idx *Index[K, V]) CollectRanges() []Segment[K, V] {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	out := make([]Segment[K, V], 0, idx.tree.Len())
	idx.tree.Ascend(func(seg segment[K, V]) bool {
		out = append(out, Segment[K, V]{Lo: seg.Lo, Hi: seg.Hi, Value: seg.Value})
		return true
	})
	return out
}
