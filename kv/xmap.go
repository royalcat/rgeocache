package kv

import (
	"github.com/puzpuzpuz/xsync"
)

type XMap[K xsync.IntegerConstraint, V any] struct {
	m *xsync.MapOf[K, V]
}

func NewXMap[K xsync.IntegerConstraint, V any]() *XMap[K, V] {
	return &XMap[K, V]{m: xsync.NewIntegerMapOf[K, V]()}
}

var _ KVS[int, any] = (*XMap[int, any])(nil)

// Get implements KVS
func (m *XMap[K, V]) Get(key K) (V, bool) {
	return m.m.Load(key)
}

// Set implements KVS
func (m *XMap[K, V]) Set(key K, value V) {
	m.m.Store(key, value)
}

func (m *XMap[K, V]) Range(f func(key K, value V) bool) {
	m.m.Range(f)
}

func (m *XMap[K, V]) Close() {}
