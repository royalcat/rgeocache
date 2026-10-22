package bordertree

import (
	"math"

	"github.com/paulmach/orb"
)

// multiPolygonContains is planar.MultiPolygonContains with precomputed ring
// bounds. planar.RingContains recomputes Ring.Bound() for every query, which is
// O(vertices) per ring and dominated cache generation time; the geometry here
// is identical, only the bounds are reused.
func multiPolygonContains(mp orb.MultiPolygon, bounds [][]orb.Bound, p orb.Point) bool {
	for i, poly := range mp {
		if len(poly) == 0 || len(bounds[i]) != len(poly) {
			continue
		}
		// The outer ring's bound rejects the vast majority of points before
		// any vertex work.
		if !bounds[i][0].Contains(p) {
			continue
		}
		if !ringContains(poly[0], bounds[i][0], p) {
			continue
		}

		inHole := false
		for j := 1; j < len(poly); j++ {
			if bounds[i][j].Contains(p) && ringContains(poly[j], bounds[i][j], p) {
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

// ringContains is planar.RingContains with an already-computed bound.
func ringContains(r orb.Ring, b orb.Bound, p orb.Point) bool {
	if len(r) < 3 || !b.Contains(p) {
		return false
	}

	c, on := rayIntersect(p, r[0], r[len(r)-1])
	if on {
		return true
	}
	for i := 0; i < len(r)-1; i++ {
		inter, on := rayIntersect(p, r[i], r[i+1])
		if on {
			return true
		}
		if inter {
			c = !c
		}
	}
	return c
}

// rayIntersect is copied from github.com/paulmach/orb/planar (MIT), where it is
// unexported. It is the same implementation planar.RingContains uses.
//
// Original implementation: http://rosettacode.org/wiki/Ray-casting_algorithm#Go
func rayIntersect(p, s, e orb.Point) (intersects, on bool) {
	if s[0] > e[0] {
		s, e = e, s
	}

	switch p[0] {
	case s[0]:
		if p[1] == s[1] {
			// p == start
			return false, true
		} else if s[0] == e[0] {
			// vertical segment (s -> e)
			// return true if within the line, check to see if start or end is greater.
			if s[1] > e[1] && s[1] >= p[1] && p[1] >= e[1] {
				return false, true
			}

			if e[1] > s[1] && e[1] >= p[1] && p[1] >= s[1] {
				return false, true
			}
		}

		// Move the y coordinate to deal with degenerate case
		p[0] = math.Nextafter(p[0], math.Inf(1))
	case e[0]:
		if p[1] == e[1] {
			// matching the end point
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

// ringBounds returns the bounds of every ring of every polygon.
func ringBounds(mp orb.MultiPolygon) [][]orb.Bound {
	out := make([][]orb.Bound, len(mp))
	for i, poly := range mp {
		out[i] = make([]orb.Bound, len(poly))
		for j, ring := range poly {
			out[i][j] = ring.Bound()
		}
	}
	return out
}
