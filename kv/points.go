package kv

import (
	"encoding/binary"
	"math"
	"os"
	"sync"
)

const pointSize = 8

// stores points as float32s, accracy lose expected
type PointsFileCache[K ~int64, V ~[2]float64] struct {
	mu sync.RWMutex

	file *os.File

	index  map[K]uint32
	writeI uint32
}

func NewPointFileCache[K ~int64, V ~[2]float64](file *os.File) *PointsFileCache[K, V] {
	return &PointsFileCache[K, V]{
		index: map[K]uint32{},
		file:  file,
	}
}

// Get implements KVS
func (m *PointsFileCache[K, V]) Get(key K) (V, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	i, ok := m.index[key]
	if !ok {
		return V{}, false
	}
	offset := int64(i) * pointSize

	v := m.readPoint(offset)

	return castToPoint64[V](v), true
}

func castToPoint64[V ~[2]float64](v [2]float32) V {
	return V([2]float64{float64(v[0]), float64(v[1])})
}

func (m *PointsFileCache[K, V]) readPoint(offset int64) (v [2]float32) {
	b := [pointSize]byte{}
	_, err := m.file.ReadAt(b[:], offset)
	if err != nil {
		panic(err)
	}

	v[0] = math.Float32frombits(binary.LittleEndian.Uint32(b[:4]))
	v[1] = math.Float32frombits(binary.LittleEndian.Uint32(b[4:]))

	return v
}

// Set implements KVS
func (m *PointsFileCache[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var b [pointSize]byte
	binary.LittleEndian.PutUint32(b[:4], math.Float32bits(float32(value[0])))
	binary.LittleEndian.PutUint32(b[4:], math.Float32bits(float32(value[1])))

	_, err := m.file.Write(b[:])
	if err != nil {
		panic(err)
	}

	m.index[key] = m.writeI
	m.writeI++
}

func (m *PointsFileCache[K, V]) Range(f func(key K, value V) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for k, offset := range m.index {
		v := m.readPoint(int64(offset))

		if !f(K(k), castToPoint64[V](v)) {
			return
		}
	}

}

func (m *PointsFileCache[K, V]) Close() {
	m.index = nil
	m.file.Close()
}
func (m *PointsFileCache[K, V]) Flush() error { return nil }
