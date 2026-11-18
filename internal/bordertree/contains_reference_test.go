package bordertree

import (
	"math"
	"math/rand"
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/simplify"
)

// These are the original, unoptimized implementations, kept only as a
// differential reference. Tests below compare the optimized versions against
// them on randomized inputs so any semantic drift is caught.

func ringContainsRef(r orb.Ring, b orb.Bound, p orb.Point) bool {
	if len(r) < 3 || !b.Contains(p) {
		return false
	}

	c, on := rayIntersectRef(p, r[0], r[len(r)-1])
	if on {
		return true
	}
	for i := 0; i < len(r)-1; i++ {
		inter, on := rayIntersectRef(p, r[i], r[i+1])
		if on {
			return true
		}
		if inter {
			c = !c
		}
	}
	return c
}

func rayIntersectRef(p, s, e orb.Point) (intersects, on bool) {
	if s[0] > e[0] {
		s, e = e, s
	}

	switch p[0] {
	case s[0]:
		if p[1] == s[1] {
			return false, true
		} else if s[0] == e[0] {
			if s[1] > e[1] && s[1] >= p[1] && p[1] >= e[1] {
				return false, true
			}
			if e[1] > s[1] && e[1] >= p[1] && p[1] >= s[1] {
				return false, true
			}
		}
		p[0] = math.Nextafter(p[0], math.Inf(1))
	case e[0]:
		if p[1] == e[1] {
			return false, true
		}
		p[0] = math.Nextafter(p[0], math.Inf(1))
	}

	if p[0] < s[0] || p[0] > e[0] {
		return false, false
	}

	if s[1] > e[1] {
		if p[1] > s[1] {
			return false, false
		} else if p[1] < e[1] {
			return true, false
		}
	} else {
		if p[1] > e[1] {
			return false, false
		} else if p[1] < s[1] {
			return true, false
		}
	}

	rs := (p[1] - s[1]) / (p[0] - s[0])
	ds := (e[1] - s[1]) / (e[0] - s[0])
	if rs == ds {
		return false, true
	}
	return rs <= ds, false
}

// TestRayIntersectEarlyRejectionsExact checks both early-rejection predicates
// against the original over random segments and points, including points
// exactly on endpoints, on segment interiors, and on their x/y coordinates.
func TestRayIntersectEarlyRejectionsExact(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	check := func(p, s, e orb.Point) {
		t.Helper()
		gi, goOn := rayIntersect(p, s, e)
		wi, woOn := rayIntersectRef(p, s, e)
		if gi != wi || goOn != woOn {
			t.Fatalf("p=%v s=%v e=%v: got (%v,%v), want (%v,%v)", p, s, e, gi, goOn, wi, woOn)
		}
	}

	for i := 0; i < 200000; i++ {
		p := orb.Point{rng.Float64()*20 - 10, rng.Float64()*20 - 10}
		s := orb.Point{rng.Float64()*20 - 10, rng.Float64()*20 - 10}
		e := orb.Point{rng.Float64()*20 - 10, rng.Float64()*20 - 10}
		check(p, s, e)

		// Snap p onto coordinates of s and e to exercise switch/endpoint paths.
		switch i % 6 {
		case 0:
			p[0] = s[0]
		case 1:
			p[0] = e[0]
		case 2:
			p[0], p[1] = s[0], s[1]
		case 3:
			p[0], p[1] = e[0], e[1]
		case 4:
			s[0] = e[0] // vertical
		case 5:
			s[1] = e[1] // horizontal
		}
		check(p, s, e)
	}
}

// TestRingContainsStillExact generates real rings (including the simplification
// step used by the border tree) and compares the optimized ringContains with
// the reference on random points and on points taken from the rings.
func TestRingContainsStillExact(t *testing.T) {
	rng := rand.New(rand.NewSource(7))

	mp := make(orb.MultiPolygon, 0, 40)
	for i := 0; i < 40; i++ {
		cx, cy := rng.Float64()*200-100, rng.Float64()*200-100
		r := orb.Ring{}
		n := 3 + rng.Intn(30)
		for j := 0; j < n; j++ {
			ang := float64(j) / float64(n) * 2 * math.Pi
			r = append(r, orb.Point{cx + math.Cos(ang)*(1+rng.Float64()*5), cy + math.Sin(ang)*(1+rng.Float64()*5)})
		}
		r = append(r, r[0]) // close the ring, like OSM multipolygons
		poly := orb.Polygon{r}
		mp = append(mp, simplify.VisvalingamKeep(10).MultiPolygon(orb.MultiPolygon{poly})[0])
		mp = append(mp, poly)
	}
	bounds := ringBounds(mp)

	check := func(p orb.Point) {
		t.Helper()
		for i, poly := range mp {
			for j, r := range poly {
				got := ringContains(r, bounds[i][j], p)
				want := ringContainsRef(r, bounds[i][j], p)
				if got != want {
					t.Fatalf("ring %d/%d p=%v: got %v want %v", i, j, p, got, want)
				}
			}
		}
	}

	for i := 0; i < 20000; i++ {
		check(orb.Point{rng.Float64()*220 - 110, rng.Float64()*220 - 110})
		if i%100 == 0 {
			for _, poly := range mp {
				for _, r := range poly {
					if len(r) > 0 {
						check(r[rng.Intn(len(r))])
					}
				}
			}
		}
	}
}

// TestIndexedRingContainsExact checks the x-bucket index against the original
// linear ringContains on randomized real-shaped rings, including holes and
// points taken from vertices and edges.
func TestIndexedRingContainsExact(t *testing.T) {
	rng := rand.New(rand.NewSource(99))

	mp := make(orb.MultiPolygon, 0, 30)
	for i := 0; i < 30; i++ {
		cx, cy := rng.Float64()*400-200, rng.Float64()*400-200
		n := ringMinIndexLength + rng.Intn(200)
		r := orb.Ring{}
		for j := 0; j < n; j++ {
			ang := float64(j) / float64(n) * 2 * math.Pi
			// Vary radius a lot so edges span many x buckets (the "wide" path).
			radius := 0.5 + rng.Float64()*20
			r = append(r, orb.Point{cx + math.Cos(ang)*radius, cy + math.Sin(ang)*radius})
		}
		r = append(r, r[0])
		mp = append(mp, orb.Polygon{r})
	}
	bounds := ringBounds(mp)
	idx := buildIndexedMultiPolygon(mp, bounds)

	check := func(p orb.Point) {
		t.Helper()
		want := multiPolygonContainsRef(mp, bounds, p)
		got := multiPolygonContainsIndexed(mp, bounds, idx, p)
		if got != want {
			t.Fatalf("p=%v: indexed=%v reference=%v", p, got, want)
		}
		for i, poly := range mp {
			for j, r := range poly {
				ri := idx[i][j]
				if ri == nil {
					continue
				}
				g := ri.contains(p)
				w := ringContainsRef(r, bounds[i][j], p)
				if g != w {
					t.Fatalf("ring %d/%d p=%v: indexed=%v reference=%v", i, j, p, g, w)
				}
			}
		}
	}

	for i := 0; i < 30000; i++ {
		check(orb.Point{rng.Float64()*440 - 220, rng.Float64()*440 - 220})
		if i%200 == 0 {
			for _, poly := range mp {
				for _, r := range poly {
					k := rng.Intn(len(r))
					check(r[k]) // vertex
					if k+1 < len(r) {
						check(orb.Point{(r[k][0] + r[k+1][0]) / 2, (r[k][1] + r[k+1][1]) / 2}) // edge midpoint
					}
				}
			}
		}
	}
}

func multiPolygonContainsRef(mp orb.MultiPolygon, bounds [][]orb.Bound, p orb.Point) bool {
	for i, poly := range mp {
		if len(poly) == 0 || len(bounds[i]) != len(poly) {
			continue
		}
		if !bounds[i][0].Contains(p) {
			continue
		}
		if !ringContainsRef(poly[0], bounds[i][0], p) {
			continue
		}
		inHole := false
		for j := 1; j < len(poly); j++ {
			if bounds[i][j].Contains(p) && ringContainsRef(poly[j], bounds[i][j], p) {
				inHole = true
				break
			}
		}
		if !inHole {
			return true
		}
	}
	return false
}
