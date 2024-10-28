package osmpbfdb

import (
	"fmt"
	"testing"

	"golang.org/x/exp/constraints"
)

func TestIndex(t *testing.T) {
	b := newIndexBuilder[int64, uint32]()

	b.Add(1, 1)
	b.Add(2, 1)
	b.Add(3, 1)
	b.Add(5, 2)
	b.Add(10, 3)
	b.Add(11, 3)

	index := b.Build()

	if err := expectInIndex(index, 1, 1); err != nil {
		t.Error(err)
	}
	if err := expectInIndex(index, 2, 1); err != nil {
		t.Error(err)
	}
	if err := expectInIndex(index, 3, 1); err != nil {
		t.Error(err)
	}
	if err := expectInIndex(index, 5, 2); err != nil {
		t.Error(err)
	}
	if err := expectInIndex(index, 10, 3); err != nil {
		t.Error(err)
	}
	if err := expectInIndex(index, 11, 3); err != nil {
		t.Error(err)
	}

	if _, ok := index.Get(4); ok {
		t.Error("expected not to find 4")
	}
	if _, ok := index.Get(6); ok {
		t.Error("expected not to find 6")
	}
	if _, ok := index.Get(0); ok {
		t.Error("expected not to find 0")
	}

	if len(index.data) != 3 {
		t.Errorf("expected 3 windows, got %d", len(index.data))
	}
}

func expectInIndex[K constraints.Integer, V comparable](index winindex[K, V], k K, v V) error {
	res, ok := index.Get(k)
	if !ok {
		return fmt.Errorf("expected to find %v", k)
	}
	if res != v {
		return fmt.Errorf("expected %v, got %v", v, res)
	}
	return nil
}
