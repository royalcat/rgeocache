package kv

import (
	"encoding/binary"
	"math"
	"os"
	"sync"
)

// stores points as float32s, accracy lose expected
type Point32FileCache[K ~int64, V ~[2]float64] struct {
	mu sync.RWMutex

	file *os.File

	index  map[K]uint32
	writeI uint32
}

func NewPointFileCache[K ~int64, V ~[2]float64](file *os.File) *Point32FileCache[K, V] {
	return &Point32FileCache[K, V]{
		index: map[K]uint32{},
		file:  file,
	}
}

// Get implements KVS
func (m *Point32FileCache[K, V]) Get(key K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	i, ok := m.index[key]
	if !ok {
		return V{}, false
	}
	offset := int64(i) * point32Size

	v := m.readPoint(offset)

	return castToPoint64[V](v), true
}

func (m *Point32FileCache[K, V]) readPoint(offset int64) (v [2]float32) {
	b := [point32Size]byte{}
	_, err := m.file.ReadAt(b[:], offset)
	if err != nil {
		panic(err)
	}

	v[0] = math.Float32frombits(binary.LittleEndian.Uint32(b[:4]))
	v[1] = math.Float32frombits(binary.LittleEndian.Uint32(b[4:]))

	return v
}

// Set implements KVS
func (m *Point32FileCache[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b [point32Size]byte
	binary.LittleEndian.PutUint32(b[:4], math.Float32bits(float32(value[0])))
	binary.LittleEndian.PutUint32(b[4:], math.Float32bits(float32(value[1])))

	_, err := m.file.Write(b[:])
	if err != nil {
		panic(err)
	}

	m.index[key] = m.writeI
	m.writeI++
}

func (m *Point32FileCache[K, V]) Range(f func(key K, value V) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for k, offset := range m.index {
		v := m.readPoint(int64(offset))

		if !f(K(k), castToPoint64[V](v)) {
			return
		}
	}

}

func (m *Point32FileCache[K, V]) Close() error {
	m.index = nil
	return m.file.Close()
}
func (m *Point32FileCache[K, V]) Flush() error { return nil }
