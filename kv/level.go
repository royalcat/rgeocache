package kv

import (
	"bytes"
	"encoding/binary"

	"github.com/syndtr/goleveldb/leveldb"
)

type Byter interface {
	ToBytes() []byte
}

type DeByter[V any] interface {
	FromBytes([]byte) V
}

type ValueBytes[V any] interface {
	Byter
	DeByter[V]
}

type BinKey interface {
	// include only KVS compatable types, to fall in main constatrains. Comparable can be extended with go update to include array types declareted in next line
	comparable
	// string and all the types that are supported by binary.Write
	~string | ~bool | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~[]bool | ~[]uint8 | ~[]int8 | ~[]int16 | ~[]uint16 | ~[]int32 | ~[]uint32 | ~[]int64 | ~[]uint64 | ~float32 | ~float64 | ~[]float32 | ~[]float64
}

type LevelDbKVS[V ValueBytes[V]] struct {
	db     *leveldb.DB
	writer *writeCache
}

func NewLevelDbKV[V ValueBytes[V]](db *leveldb.DB) *LevelDbKVS[V] {
	writer := newWriteCache(db)
	writer.Run()
	return &LevelDbKVS[V]{
		db:     db,
		writer: writer,
	}
}

// Set implements KVS
func (kvs *LevelDbKVS[V]) Set(key int64, value V) {
	keyB := keyBytes(key)
	newValue := value.ToBytes()

	if ok, err := kvs.db.Has(keyB, nil); err == nil && ok {
		if cachedValue, err := kvs.db.Get(keyB, nil); err == nil && bytes.Equal(cachedValue, newValue) {
			return
		}
	}

	kvs.writer.Put(key, newValue)
}

// Get implements KVS
func (kvs *LevelDbKVS[V]) Get(key int64) (V, bool) {
	kvs.writer.Flush()

	var value V
	body, err := kvs.db.Get(keyBytes(key), nil)
	if err != nil {
		return value, false
	}
	value.FromBytes(body)
	return value, true
}

func (kvs *LevelDbKVS[V]) Range(iterCall func(key int64, value V) bool) {
	iter := kvs.db.NewIterator(nil, nil)
	defer iter.Release()

	for iter.Next() {
		k := bytesToKey(iter.Key())
		var v V
		v.FromBytes(iter.Value())

		if !iterCall(k, v) {
			break
		}
	}
}

func (kvs *LevelDbKVS[V]) Close() {
	kvs.writer.Close()
	kvs.writer.Flush()
	kvs.db.Close()
}

func keyBytes(key int64) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(key))
	return buf
}

func bytesToKey(buf []byte) int64 {
	return int64(binary.LittleEndian.Uint64(buf))
}
