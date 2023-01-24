package kv

import (
	"bytes"
	"encoding/binary"
	"log"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
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

type LevelDbKVS[K BinKey, V ValueBytes[V]] struct {
	db     *leveldb.DB
	writer *writeCache[V]
}

func NewLevelDbKV[K BinKey, V ValueBytes[V]](db *leveldb.DB) *LevelDbKVS[K, V] {
	writer := newWriteCache[V](db)
	writer.Run()
	return &LevelDbKVS[K, V]{
		db:     db,
		writer: writer,
	}
}

// Set implements KVS
func (kvs *LevelDbKVS[K, V]) Set(key int64, value V) {
	kvs.writer.Put(key, value)
}

// Get implements KVS
func (kvs *LevelDbKVS[K, V]) Get(key int64) (V, bool) {
	kvs.writer.Flush()

	var value V
	body, err := kvs.db.Get(keyBytes(key), &opt.ReadOptions{})
	if err != nil {
		return value, false
	}
	value.FromBytes(body)
	return value, true
}

// Close implements KVS
func (kvs *LevelDbKVS[K, V]) Close() {
	kvs.writer.Close()
	kvs.db.Close()
}

func (kvs *LevelDbKVS[K, V]) Range(f func(key int64, value V) bool) {
	panic("unimplemented")
}

func keyBytes[K BinKey](key K) []byte {
	buf := new(bytes.Buffer)
	err := write(buf, key)
	if err != nil {
		log.Fatalf(err.Error())
	}
	return buf.Bytes()
}

// dirty hack to write string keys, wait for go update to remove it
func write(buf *bytes.Buffer, data any) error {
	switch data.(type) {
	case string:
		_, err := buf.WriteString(data.(string))
		return err
	default:
		return binary.Write(buf, binary.LittleEndian, data)
	}
}
