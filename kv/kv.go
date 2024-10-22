package kv

import "io"

type KVS[K comparable, V any] interface {
	Get(key K) (V, bool)
	Set(key K, value V)
	Range(func(key K, value V) bool)
	Flush() error

	io.Closer
}
