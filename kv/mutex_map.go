package kv

import "sync"

type MutexMap[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

func NewMutexMap[K comparable, V any]() *MutexMap[K, V] {
	return &MutexMap[K, V]{m: make(map[K]V)}
}

var _ KVS[string, any] = (*MutexMap[string, any])(nil)

// Get implements KVS
func (m *MutexMap[K, V]) Get(key K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	v, ok := m.m[key]
	return v, ok
}

// Set implements KVS
func (m *MutexMap[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.m[key] = value
}

func (m *MutexMap[K, V]) Range(f func(key K, value V) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.m {
		if !f(k, v) {
			return
		}
	}
}

func (m *MutexMap[K, V]) Close() {}
