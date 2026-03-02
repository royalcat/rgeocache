package savev1

import (
	"slices"
	"testing"
)

func TestDedupeMap(t *testing.T) {
	// Test adding unique values
	uq := newUniqueMap()

	s := uq.Slice()
	if len(s) != 1 && s[0] != "" {
		t.Error("Expected zero value to have an empty string as first value")
	}

	fooIndex := uq.Add("foo")
	barIndex := uq.Add("bar")
	bazIndex := uq.Add("baz")

	s = uq.Slice()
	if len(s) != 4 {
		t.Errorf("Expected 4 unique values, got %d", len(s))
	}
	if slices.Index(s, "foo") != fooIndex || slices.Index(s, "bar") != barIndex || slices.Index(s, "baz") != bazIndex {
		t.Error("Expected indices to be 1, 2, 3")
	}

	// Test adding duplicate values
	fooIndex = uq.Add("foo")
	barIndex = uq.Add("bar")
	bazIndex = uq.Add("baz")

	s = uq.Slice()
	if len(s) != 4 {
		t.Errorf("Expected 4 unique values, got %d", len(s))
	}
	if slices.Index(s, "foo") != fooIndex || slices.Index(s, "bar") != barIndex || slices.Index(s, "baz") != bazIndex {
		t.Error("Expected indices to be 1, 2, 3")
	}

	// Test adding empty string
	zeroIndex := uq.Add("")
	if slices.Index(s, "") != zeroIndex {
		t.Error("Expected empty string to have index 0")
	}
	if zeroIndex != 0 {
		t.Error("Expected empty string to have index 0")
	}

	zeroIndex = uq.Add("")
	if slices.Index(s, "") != zeroIndex {
		t.Error("Expected empty string to have index 0")
	}
	if zeroIndex != 0 {
		t.Error("Expected empty string to have index 0")
	}

	s = uq.Slice()
	if len(s) != 4 {
		t.Errorf("Expected 4 unique values, got %d", len(s))
	}
}
