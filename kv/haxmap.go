package kv

import (
	"github.com/alphadose/haxmap"
	"golang.org/x/exp/constraints"
)

func NewHaxMap[K constraints.Ordered, V any]() *HaxMap[K, V] {
	return &HaxMap[K, V]{Map: haxmap.New[K, V]()}
}

var _ KVS[string, any] = (*MutexMap[string, any])(nil)

type HaxMap[K constraints.Integer | constraints.Float | constraints.Complex | ~string | uintptr, V any] struct {
	*haxmap.Map[K, V]
}

var _ KVS[int64, any] = (*HaxMap[int64, any])(nil)

// // Get implements KVS
// func (m *HaxMap[K, V]) Get(key K) (V, bool) {
// 	return m.M.Get(key)
// }

// // Set implements KVS
// func (m *HaxMap[K, V]) Set(key K, value V) {
// 	m.M.Set(key, value)
// }

func (m *HaxMap[K, V]) Range(f func(key K, value V) bool) {
	m.Map.ForEach(f)
}

func (m *HaxMap[K, V]) Close() {
	m.Clear()
}
