package osmpbfdb

import (
	"cmp"
	"slices"

	"golang.org/x/exp/constraints"
)

type window[K constraints.Integer, V comparable] struct {
	minK K
	maxK K
	v    V
}

type indexBuilder[K constraints.Integer, V comparable] struct {
	currentWindow *window[K, V]
	data          []window[K, V]
}

func newIndexBuilder[K constraints.Integer, V comparable]() *indexBuilder[K, V] {
	return &indexBuilder[K, V]{}
}

func (b *indexBuilder[K, V]) Add(k K, v V) {
	if b.currentWindow == nil {
		b.currentWindow = &window[K, V]{minK: k, maxK: 1, v: v}
		return
	}

	if b.currentWindow.maxK+1 == k && b.currentWindow.v == v {
		b.currentWindow.maxK = k
		return
	} else {
		b.data = append(b.data, *b.currentWindow)
		b.currentWindow = &window[K, V]{minK: k, maxK: k, v: v}
		return
	}
}

func (b *indexBuilder[K, V]) Build() winindex[K, V] {
	if b.currentWindow != nil {
		b.data = append(b.data, *b.currentWindow)
	}

	slices.SortFunc(b.data, func(a, b window[K, V]) int {
		return cmp.Compare(a.maxK, b.minK)
	})

	index := winindex[K, V]{
		data: b.data,
	}
	b.data = nil
	return index
}

type winindex[K constraints.Integer, V comparable] struct {
	data []window[K, V]
}

func (bi winindex[K, V]) Get(k K) (V, bool) {
	i, ok := slices.BinarySearchFunc(bi.data, window[K, V]{minK: k}, func(a, b window[K, V]) int {
		if k < a.minK {
			return 1
		}
		if k > a.maxK {
			return -1
		}
		return 0
	})
	if !ok {
		var v V
		return v, false
	}

	if k < bi.data[i].minK || k > bi.data[i].maxK {
		var v V
		return v, false
	}

	return bi.data[i].v, true
}
