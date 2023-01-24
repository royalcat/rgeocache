package kv

import (
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
)

type val[V Byter] struct {
	Key   int64
	Value V
}

type writeCache[V Byter] struct {
	in         chan val[V]
	db         *leveldb.DB
	batchMutex sync.Mutex
	batch      *leveldb.Batch
	Buf        int
}

const defaultWriteCacheSize = 1024 * 1024

func newWriteCache[V Byter](db *leveldb.DB) *writeCache[V] {
	w := &writeCache[V]{
		in:    make(chan val[V], defaultWriteCacheSize),
		db:    db,
		batch: &leveldb.Batch{},
		Buf:   defaultWriteCacheSize,
	}

	return w
}

func (w *writeCache[V]) Run() {
	go func() {
		for p := range w.in {
			w.batchMutex.Lock()
			w.batch.Put(keyBytes(p.Key), p.Value.ToBytes())
			w.batchMutex.Unlock()
			if w.batch.Len() > w.Buf {
				w.Flush()
			}
		}
		w.Flush()
	}()
}

func (w *writeCache[V]) Flush() {
	if w.batch.Len() > 0 {
		w.batchMutex.Lock()
		w.db.Write(w.batch, nil)
		w.batch.Reset()
		w.batchMutex.Unlock()
	}
}

func (w *writeCache[V]) Put(key int64, value V) {
	w.in <- val[V]{key, value}
}

func (w *writeCache[V]) Close() {
	close(w.in)
}
