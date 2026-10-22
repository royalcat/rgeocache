// Package idset provides a compact set of object ids used for duplicate
// detection during cache generation.
package idset

import (
	"math"
	"sync"

	"github.com/RoaringBitmap/roaring/v2"
)

// Set is a thread-safe set of int64 ids. Ids that fit in uint32 are stored in
// a roaring bitmap (a few bytes per id even for sparse OSM id ranges); anything
// else falls back to a map.
type Set struct {
	mu   sync.Mutex
	bits *roaring.Bitmap
	big  map[int64]struct{}
}

func New() *Set {
	return &Set{bits: roaring.New()}
}

// Add inserts id and reports whether it was absent before.
func (s *Set) Add(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id >= 0 && id <= math.MaxUint32 {
		return s.bits.CheckedAdd(uint32(id))
	}
	if s.big == nil {
		s.big = make(map[int64]struct{})
	}
	if _, ok := s.big[id]; ok {
		return false
	}
	s.big[id] = struct{}{}
	return true
}

// Contains reports whether id is present.
func (s *Set) Contains(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id >= 0 && id <= math.MaxUint32 {
		return s.bits.Contains(uint32(id))
	}
	_, ok := s.big[id]
	return ok
}

// Len returns the number of stored ids.
func (s *Set) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int(s.bits.GetCardinality()) + len(s.big)
}
