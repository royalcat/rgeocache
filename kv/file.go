package kv

import (
	"os"
	"sync"
)

type block struct {
	offset uint32
	size   uint32
}

type FileKVS[K ~int64, V ValueBytes[V]] struct {
	mu          sync.RWMutex
	offsets     map[uint64]block
	file        *os.File
	writeOffset uint64
}

func NewFileKV[K ~int64, V ValueBytes[V]](file *os.File) *FileKVS[K, V] {
	return &FileKVS[K, V]{
		offsets: make(map[uint64]block),
		file:    file,
	}
}

// Get implements KVS
func (m *FileKVS[K, V]) Get(key K) (v V, ok bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	block, ok := m.offsets[uint64(key)]
	if !ok {
		return v, false
	}

	data := make([]byte, block.size)
	_, err := m.file.ReadAt(data, int64(block.offset))
	if err != nil {
		panic(err)
	}

	v = v.FromBytes(data)
	return v, true
}

// Set implements KVS
func (m *FileKVS[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data := value.ToBytes()
	size, err := m.file.Write(data)
	if err != nil {
		panic(err)
	}
	m.offsets[uint64(key)] = block{
		offset: uint32(m.writeOffset),
		size:   uint32(size),
	}
	m.writeOffset += uint64(size)
}

func (m *FileKVS[K, V]) Range(f func(key K, value V) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for k, block := range m.offsets {
		data := make([]byte, block.size)
		_, err := m.file.ReadAt(data, int64(block.offset))
		if err != nil {
			panic(err)
		}
		var val V
		val = val.FromBytes(data)

		if !f(K(k), val) {
			return
		}
	}
}

func (m *FileKVS[K, V]) Close() {
	m.offsets = nil
	m.file.Close()
}
func (m *FileKVS[K, V]) Flush() error { return nil }
