package kv

import "sync"

type Point32MutexMap[K comparable, V ~[2]float64] struct {
	mu sync.RWMutex
	m  map[K]point32
}

func NewPoints32MutexMap[K comparable, V ~[2]float64]() *Point32MutexMap[K, V] {
	return &Point32MutexMap[K, V]{
		m: map[K]point32{},
	}
}

var _ KVS[int64, [2]float64] = (*Point32MutexMap[int64, [2]float64])(nil)

// Get implements KVS.
func (f *Point32MutexMap[K, V]) Get(key K) (V, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()

	v, ok := f.m[key]
	return castToPoint64[V](v), ok
}

// Set implements KVS.
func (m *Point32MutexMap[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.m[key] = castToPoint32(value)
}

// Range implements KVS.
func (m *Point32MutexMap[K, V]) Range(f func(key K, value V) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for k, v := range m.m {
		if !f(K(k), castToPoint64[V](v)) {
			return
		}
	}
}

// Close implements KVS.
func (f *Point32MutexMap[K, V]) Close() error { return nil }

// Flush implements KVS.
func (f *Point32MutexMap[K, V]) Flush() error { return nil }
