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

func TestAppendPoints(t *testing.T) {
	mainDir, srcDir := t.TempDir(), t.TempDir()

	w, err := Create(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WritePoint(Point{X: 1, Y: 2, NameID: 7, NodeKey: 42, ClaimSeq: 3}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	sw, err := Create(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := sw.WritePoint(Point{X: float64(10 + i), Y: 20, NameID: uint32(i + 1), StreetID: 1, Weight: 10}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sw.Close(); err != nil {
		t.Fatal(err)
	}
	src, err := Open(srcDir)
	if err != nil {
		t.Fatal(err)
	}

	n, err := AppendPoints(mainDir, src, func(p *Point) {
		p.NameID += 100
		p.StreetID += 200
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("appended %d points, want 3", n)
	}

	r, err := Open(mainDir)
	if err != nil {
		t.Fatal(err)
	}
	if r.NumPoints() != 4 {
		t.Fatalf("got %d points, want 4", r.NumPoints())
	}
	var got []Point
	if err := r.IteratePoints(func(_ int, p Point) error {
		got = append(got, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got[0].NameID != 7 || got[0].NodeKey != 42 || got[0].ClaimSeq != 3 {
		t.Fatalf("existing point changed: %+v", got[0])
	}
	for i := 1; i < 4; i++ {
		want := Point{X: float64(10 + i - 1), Y: 20, NameID: uint32(i) + 100, StreetID: 201, Weight: 10}
		if got[i] != want {
			t.Fatalf("appended point %d: got %+v, want %+v", i, got[i], want)
		}
	}
}
