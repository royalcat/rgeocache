package osmpbfdb

import (
	"cmp"
	"slices"
)

type pair[K any, V any] struct {
	k K
	v V
}

type indexBuilder[K cmp.Ordered, V any] struct {
	data []pair[K, V]
}

func NewIndexBuilder[K cmp.Ordered, V any]() *indexBuilder[K, V] {
	return &indexBuilder[K, V]{}
}

func (b *indexBuilder[K, V]) Add(k K, v V) {
	b.data = append(b.data, pair[K, V]{k, v})
}

func (b *indexBuilder[K, V]) Build() bindex[K, V] {
	slices.SortFunc(b.data, func(a pair[K, V], b pair[K, V]) int {
		return cmp.Compare(a.k, b.k)
	})

	index := bindex[K, V]{
		data: b.data,
	}
	b.data = nil
	return index
}

type bindex[K cmp.Ordered, V any] struct {
	data []pair[K, V]
}

func (bi bindex[K, V]) Get(k K) (V, bool) {
	i, ok := slices.BinarySearchFunc(bi.data, pair[K, V]{k: k}, func(a, b pair[K, V]) int {
		return cmp.Compare(a.k, b.k)
	})
	if !ok {
		var v V
		return v, false
	}

	return bi.data[i].v, true
}
