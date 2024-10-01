package kv

import (
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
)

type val struct {
	Key   int64
	Value []byte
}

type writeCache struct {
	in         chan val
	db         *leveldb.DB
	batchMutex sync.Mutex
	batch      *leveldb.Batch
	Buf        int
}

const defaultWriteCacheSize = 1024 * 1024

func newWriteCache(db *leveldb.DB) *writeCache {
	w := &writeCache{
		in:    make(chan val, defaultWriteCacheSize),
		db:    db,
		batch: &leveldb.Batch{},
		Buf:   defaultWriteCacheSize,
	}

	return w
}

func (w *writeCache) Run() {
	go func() {
		for p := range w.in {
			w.batchMutex.Lock()
			w.batch.Put(keyBytes(p.Key), p.Value)
			w.batchMutex.Unlock()
			if w.batch.Len() > w.Buf {
				w.Flush()
			}
		}
		w.Flush()
	}()
}

func (w *writeCache) Flush() {
	if w.batch.Len() > 0 {
		w.batchMutex.Lock()
		w.db.Write(w.batch, nil)
		w.batch.Reset()
		w.batchMutex.Unlock()
	}
}

func (w *writeCache) Put(key int64, value []byte) {
	w.in <- val{key, value}
}

func (w *writeCache) Close() {
	close(w.in)
}
