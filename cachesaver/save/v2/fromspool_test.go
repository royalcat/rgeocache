package savev2

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"
	"unique"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/internal/spool"
	"github.com/royalcat/rgeocache/internal/strtab"
	"github.com/royalcat/rgeocache/kdbush"
	"golang.org/x/exp/mmap"
)

// TestWriteSpoolMatchesSave builds the same tiny cache through the legacy
// in-memory path and through the spool path, then compares the decoded points
// and edges.
func TestWriteSpoolMatchesSave(t *testing.T) {
	meta := cachemodel.Metadata{Version: 1, DateCreated: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}

	const (
		street = "Main Street"
		city   = "Town"
		region = "Region"
		name   = "Building"
	)

	// Legacy path: one building plus three collinear road shape points.
	points := []cachemodel.Point{
		{X: 10.0, Y: 20.0, Data: cachemodel.Info{
			Name:        unique.Make(name),
			Street:      unique.Make(street),
			HouseNumber: unique.Make("1"),
			City:        unique.Make(city),
			Region:      unique.Make(region),
			Weight:      10,
			Type:        cachemodel.GeoObjectBuilding,
		}},
		{X: 10.0, Y: 20.0001, Data: roadInfo(street, city, region)},
		{X: 10.0, Y: 20.0002, Data: roadInfo(street, city, region)},
		{X: 10.0, Y: 20.0003, Data: roadInfo(street, city, region)},
	}
	items := []cachemodel.Item{
		{Kind: cachemodel.ItemPoint, Point: points[0]},
		{Kind: cachemodel.ItemPoint, Point: points[1], GraphNode: 1},
		{Kind: cachemodel.ItemPoint, Point: points[2], GraphNode: 2},
		{Kind: cachemodel.ItemPoint, Point: points[3], GraphNode: 3},
		{Kind: cachemodel.ItemEdge, Edge: cachemodel.GraphEdge{
			FromNode: 1, ToNode: 2, Street: unique.Make(street), Name: unique.Make(""), Class: 6, Oneway: 0,
		}},
		{Kind: cachemodel.ItemEdge, Edge: cachemodel.GraphEdge{
			FromNode: 2, ToNode: 3, Street: unique.Make(street), Name: unique.Make(""), Class: 6, Oneway: 0,
		}},
	}
	var legacy bytes.Buffer
	if err := Save(&legacy, slices.Values(items), slices.Values([]cachemodel.Zone{}), meta); err != nil {
		t.Fatal(err)
	}

	// Spool path: same logical stream, occurrences instead of graph node ids.
	dir := t.TempDir()
	sp, err := spool.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	table := strtab.New()
	write := func(x, y float64, weight uint8, typ cachemodel.GeoObjectType, nodeKey uint64, claim uint32, house, pointName string) {
		if err := sp.WritePoint(spool.Point{
			X: x, Y: y,
			NameID:        table.Add(pointName),
			StreetID:      table.Add(street),
			HouseNumberID: table.Add(house),
			CityID:        table.Add(city),
			RegionID:      table.Add(region),
			Weight:        weight,
			GeoType:       uint8(typ),
			NodeKey:       nodeKey,
			ClaimSeq:      claim,
		}); err != nil {
			t.Fatal(err)
		}
	}
	write(10.0, 20.0, 10, cachemodel.GeoObjectBuilding, 0, 0, "1", name)
	write(10.0, 20.0001, 5, cachemodel.GeoObjectRoad, 1, 1, "", "")
	write(10.0, 20.0002, 5, cachemodel.GeoObjectRoad, 2, 2, "", "")
	write(10.0, 20.0003, 5, cachemodel.GeoObjectRoad, 3, 3, "", "")
	for i := uint32(1); i < 3; i++ {
		if err := sp.WriteEdge(spool.Edge{
			From: i, To: i + 1,
			StreetID: table.Add(street), NameID: table.Add(""),
			Class: 6, Oneway: 0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	sr, err := spool.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var spooled bytes.Buffer
	if err := WriteSpool(&spooled, sr, table, nil, meta, dir); err != nil {
		t.Fatal(err)
	}

	legacyFile := writeTempCache(t, legacy.Bytes())
	spooledFile := writeTempCache(t, spooled.Bytes())

	a := loadPoints(t, legacyFile)
	b := loadPoints(t, spooledFile)
	if len(a) != len(b) {
		t.Fatalf("point count mismatch: legacy %d, spool %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("point %d mismatch: %+v vs %+v", i, a[i], b[i])
		}
	}

	ae := loadEdges(t, legacyFile)
	be := loadEdges(t, spooledFile)
	if len(ae) != len(be) {
		t.Fatalf("edge count mismatch: legacy %d, spool %d", len(ae), len(be))
	}
	for i := range ae {
		if ae[i] != be[i] {
			t.Fatalf("edge %d mismatch: %+v vs %+v", i, ae[i], be[i])
		}
	}
	if len(be) != 1 {
		t.Fatalf("expected one simplified edge, got %d", len(be))
	}
}

func TestCanonicalizePicksLowestClaimSeq(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Three occurrences of the same OSM node; the second was encountered first.
	for _, seq := range []uint32{7, 2, 5} {
		if err := sp.WritePoint(spool.Point{X: float64(seq), Y: 0, NodeKey: 42, ClaimSeq: seq}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	sr, err := spool.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	canonical, groups, err := canonicalizePoints(sr, dir)
	if err != nil {
		t.Fatal(err)
	}
	if groups != 1 {
		t.Fatalf("canonicalized %d groups, want 1", groups)
	}
	for _, occ := range []int{0, 1, 2} {
		if canonical[occ] != 1 {
			t.Fatalf("occurrence %d canonicalized to %d, want 1", occ, canonical[occ])
		}
	}
}

func TestCanonicalizePrefersNamedStreet(t *testing.T) {
	dir := t.TempDir()
	sp, err := spool.Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	table := strtab.New()
	street := table.Add("Named street")
	for _, p := range []spool.Point{
		{X: 0, Y: 0, NodeKey: 42, ClaimSeq: 1},
		{X: 0, Y: 0, NodeKey: 42, ClaimSeq: 2, StreetID: street},
		{X: 0, Y: 0, NodeKey: 42, ClaimSeq: 3},
	} {
		if err := sp.WritePoint(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	sr, err := spool.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := canonicalizePoints(sr, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, occ := range []int{0, 1, 2} {
		if canonical[occ] != 1 {
			t.Fatalf("occurrence %d canonicalized to %d, want named occurrence 1", occ, canonical[occ])
		}
	}
}

func roadInfo(street, city, region string) cachemodel.Info {
	return cachemodel.Info{
		Name:        unique.Make(""),
		Street:      unique.Make(street),
		HouseNumber: unique.Make(""),
		City:        unique.Make(city),
		Region:      unique.Make(region),
		Weight:      5,
		Type:        cachemodel.GeoObjectRoad,
	}
}

func writeTempCache(t *testing.T, body []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "kdbh-*.rgc")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	if _, err := f.Write([]byte{'R', 'G', 'E', 'O', 2, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func loadPoints(t *testing.T, path string) []kdbush.Point[V2PointData] {
	t.Helper()
	ra, err := mmap.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ra.Close() }()
	res, err := LoadMmap(ra)
	if err != nil {
		t.Fatal(err)
	}
	var out []kdbush.Point[V2PointData]
	if err := res.DiskBush.ForEach(func(p kdbush.Point[V2PointData]) bool {
		out = append(out, p)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func loadEdges(t *testing.T, path string) []string {
	t.Helper()
	ra, err := mmap.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ra.Close() }()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	gs, err := FindGraphSection(ra, st.Size())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, gs.EdgeCount)
	for i := uint64(0); i < gs.EdgeCount; i++ {
		rec, err := gs.Edge(ra, i)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d-%d:%d:%d:%d:%d", rec.FromPos, rec.ToPos, rec.StreetID, rec.NameID, rec.Class, rec.Oneway))
	}
	return out
}
