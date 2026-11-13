// Command cachediff compares the decoded content of two v2 caches: the point
// multiset (coordinates, resolved strings, weight, type), the zones and the
// road graph edges. Generation is content-deterministic regardless of how the
// file is laid out, so this is the way to check that a change did not alter
// what a cache contains. Edges are compared direction-normalized: an
// undirected edge matches in either direction, a one-way edge only in its
// travel direction.
//
// Usage: cachediff A.rgc B.rgc [edge-substring]
//
// The optional substring prints every edge of either cache containing it, to
// inspect a difference. The exit status is 1 when the contents differ.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/kdbush"
	"golang.org/x/exp/mmap"
)

type content struct {
	points, zones, edges []string
	meta                 string
}

func load(path string) content {
	r, err := mmap.Open(path)
	if err != nil {
		panic(err)
	}
	res, err := savev2.LoadMmap(r)
	if err != nil {
		panic(err)
	}
	strs, err := res.Strings()
	if err != nil {
		panic(err)
	}
	var c content
	type xy struct{ x, y float64 }
	pos := map[int]xy{}
	if err := res.DiskBush.ForEachPos(func(p int, pt kdbush.Point[savev2.V2PointData]) bool {
		pos[p] = xy{pt.X, pt.Y}
		d := pt.Data
		c.points = append(c.points, fmt.Sprintf("%.9f,%.9f|%s|%s|%s|%s|%s|%d|%d", pt.X, pt.Y,
			strs[d.NameID], strs[d.StreetID], strs[d.HouseNumberID], strs[d.CityID], strs[d.RegionID], d.Weight, d.GeoType))
		return true
	}); err != nil {
		panic(err)
	}
	for _, z := range res.Zones {
		c.zones = append(c.zones, fmt.Sprintf("%d|%s|%v|%v", z.Type, z.Name.Value(), z.Bounds, z.Polygon))
	}
	sort.Strings(c.zones)
	st, _ := os.Stat(path)
	g, err := savev2.FindGraphSection(r, st.Size())
	if err != nil {
		panic(err)
	}
	if err := g.ForEachEdge(r, func(e savev2.GraphEdgeRecord) bool {
		a, b := pos[int(e.FromPos)], pos[int(e.ToPos)]
		oneway := e.Oneway
		if oneway == 2 { // backward -> forward
			a, b = b, a
			oneway = 1
		}
		if oneway == 0 && (a.x > b.x || (a.x == b.x && a.y > b.y)) {
			a, b = b, a
		}
		c.edges = append(c.edges, fmt.Sprintf("%.9f,%.9f>%.9f,%.9f|%s|%s|%d|%d", a.x, a.y, b.x, b.y,
			strs[e.StreetID], strs[e.NameID], e.Class, oneway))
		return true
	}); err != nil {
		panic(err)
	}
	c.meta = fmt.Sprintf("%v", res.Metadata)
	sort.Strings(c.points)
	sort.Strings(c.edges)
	return c
}

func diff(name string, a, b []string) bool {
	fmt.Printf("%s: %d vs %d", name, len(a), len(b))
	i, j, onlyA, onlyB := 0, 0, 0, 0
	var sa, sb string
	for i < len(a) || j < len(b) {
		switch {
		case j >= len(b) || (i < len(a) && a[i] < b[j]):
			if onlyA == 0 {
				sa = a[i]
			}
			onlyA++
			i++
		case i >= len(a) || a[i] > b[j]:
			if onlyB == 0 {
				sb = b[j]
			}
			onlyB++
			j++
		default:
			i++
			j++
		}
	}
	fmt.Printf("  only-in-A=%d only-in-B=%d\n", onlyA, onlyB)
	if onlyA > 0 {
		fmt.Println("   A:", sa)
	}
	if onlyB > 0 {
		fmt.Println("   B:", sb)
	}
	return onlyA == 0 && onlyB == 0
}

func main() {
	a, b := load(os.Args[1]), load(os.Args[2])
	fmt.Println("meta A:", a.meta)
	fmt.Println("meta B:", b.meta)
	if len(os.Args) > 3 {
		for n, c := range map[string]content{"A": a, "B": b} {
			for _, e := range c.edges {
				if strings.Contains(e, os.Args[3]) {
					fmt.Println(n, e)
				}
			}
		}
	}
	ok := diff("points", a.points, b.points)
	ok = diff("zones", a.zones, b.zones) && ok
	ok = diff("edges", a.edges, b.edges) && ok
	if !ok {
		os.Exit(1)
	}
	fmt.Println("DECODED CONTENT IDENTICAL")
}
