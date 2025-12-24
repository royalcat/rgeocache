package geoparser

import "sync"

type set[K comparable] struct {
	mu    sync.RWMutex
	items map[K]struct{}
}

func newSet[K comparable]() *set[K] {
	return &set[K]{
		items: make(map[K]struct{}),
	}
}

func (s *set[K]) Add(item K) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[item] = struct{}{}
}

func (s *set[K]) Contains(item K) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.items[item]
	return ok
}

func (s *set[K]) Remove(item K) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, item)
}

func (s *set[K]) ContainsOrAdd(item K) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[item]; !ok {
		s.items[item] = struct{}{}
		return true
	}
	return false
}
