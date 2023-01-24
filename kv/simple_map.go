package kv

type Map[K comparable, V any] struct {
	m map[K]V
}

func NewMap[K comparable, V any]() *MutexMap[K, V] {
	return &MutexMap[K, V]{m: make(map[K]V)}
}

var _ KVS[string, any] = (*MutexMap[string, any])(nil)

// Get implements KVS
func (m *Map[K, V]) Get(key K) (V, bool) {

	v, ok := m.m[key]
	return v, ok
}

// Set implements KVS
func (m *Map[K, V]) Set(key K, value V) {
	m.m[key] = value
}

func (m *Map[K, V]) Range(f func(key K, value V) bool) {
	for k, v := range m.m {
		if !f(k, v) {
			return
		}
	}
}

func (m *Map[K, V]) Close() {}
