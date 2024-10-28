// Package syncpool provides a generic wrapper around sync.Pool
package osmpbfdb

import (
	"sync"
)

// A syncpool is a generic wrapper around a sync.syncpool.
type syncpool[T any] struct {
	pool sync.Pool
}

// newSyncPool creates a new Pool with the provided new function.
//
// The equivalent sync.Pool construct is "sync.Pool{newSyncPool: fn}"
func newSyncPool[T any](fn func() T) syncpool[T] {
	return syncpool[T]{
		pool: sync.Pool{New: func() interface{} { return fn() }},
	}
}

// Get is a generic wrapper around sync.Pool's Get method.
func (p *syncpool[T]) Get() T {
	return p.pool.Get().(T)
}

// Get is a generic wrapper around sync.Pool's Put method.
func (p *syncpool[T]) Put(x T) {
	p.pool.Put(x)
}
