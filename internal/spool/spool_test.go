package spool

import (
	"testing"
)

func TestPairSorterExternalMerge(t *testing.T) {
	dir := t.TempDir()
	// A tiny buffer forces several spilled runs.
	s, err := NewPairSorter(dir, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	in := []Pair{
		{Key: 2, Seq: 5, Occ: 0},
		{Key: 1, Seq: 9, Occ: 1},
		{Key: 2, Seq: 1, Occ: 2},
		{Key: 1, Seq: 2, Occ: 3},
		{Key: 1, Seq: 9, Occ: 4},
		{Key: 3, Seq: 0, Occ: 5},
		{Key: 2, Seq: 1, Occ: 6},
	}
	for _, p := range in {
		if err := s.Add(p.Key, p.Seq, p.Occ); err != nil {
			t.Fatal(err)
		}
	}

	var got []Pair
	if err := s.Iterate(func(p Pair) error {
		got = append(got, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := []Pair{
		{Key: 1, Seq: 2, Occ: 3},
		{Key: 1, Seq: 9, Occ: 1},
		{Key: 1, Seq: 9, Occ: 4},
		{Key: 2, Seq: 1, Occ: 2},
		{Key: 2, Seq: 1, Occ: 6},
		{Key: 2, Seq: 5, Occ: 0},
		{Key: 3, Seq: 0, Occ: 5},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d pairs, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("pair %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestPointEdgeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	in := Point{
		X: 1.5, Y: -2.25,
		NameID: 1, StreetID: 2, HouseNumberID: 3, CityID: 4, RegionID: 5,
		Weight: 7, GeoType: 2, NodeKey: 1<<63 | 9, ClaimSeq: 42,
	}
	if err := w.WritePoint(in); err != nil {
		t.Fatal(err)
	}
	e := Edge{From: 1, To: 2, StreetID: 3, NameID: 4, Class: 5, Oneway: 6}
	if err := w.WriteEdge(e); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.NumPoints() != 1 || r.NumEdges() != 1 {
		t.Fatalf("counts: %d points, %d edges", r.NumPoints(), r.NumEdges())
	}
	if err := r.IteratePoints(func(occ int, p Point) error {
		if occ != 0 || p != in {
			t.Fatalf("point %d: got %+v, want %+v", occ, p, in)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.IterateEdges(func(i int, got Edge) error {
		if i != 0 || got != e {
			t.Fatalf("edge %d: got %+v, want %+v", i, got, e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
