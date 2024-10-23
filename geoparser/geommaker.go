package geoparser

import (
	"errors"

	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
)

func (f *GeoGen) makeLineString(nodes osm.WayNodes) orb.LineString {
	ls := orb.LineString{}
	for _, node := range nodes {

		if node.Lat != 0 && node.Lon != 0 {
			ls = append(ls, orb.Point{node.Lon, node.Lat})
		} else {
			p, err := f.osmdb.GetNode(node.ID)
			if err != nil {
				f.log.WithError(err).Error("failed to get node")
				continue
			}

			if p.Lat == 0 && p.Lon == 0 {
				f.log.Error("node has no coordinates")
				continue
			}

			ls = append(ls, orb.Point{p.Lon, p.Lat})
		}

	}
	return ls
}

func (f *GeoGen) buildPolygon(members osm.Members) (orb.MultiPolygon, error) {

	var outer []segment
	var inner []segment

	outerCount := 0

	for _, m := range members {
		if m.Type != osm.TypeWay {
			continue
		}
		if m.Role != "inner" && m.Role != "outer" {
			continue
		}

		if m.Role == "outer" {
			outerCount++
		}

		way, err := f.osmdb.GetWay(osm.WayID(m.Ref))
		if err != nil || len(way.Nodes) == 0 {
			// we have the way but none the the node members
			continue
		}

		ls := f.makeLineString(way.Nodes)
		segment := segment{
			Orientation: m.Orientation,
			Line:        ls,
		}

		if m.Role == "outer" {
			// if segment.Orientation == orb.CW {
			// 	segment.Reverse()
			// }
			//segment.Reverse()

			outer = append(outer, segment)
		} else {
			// if segment.Orientation == orb.CCW {
			// 	segment.Reverse()
			// }
			//segment.Reverse()

			inner = append(inner, segment)
		}
	}

	if len(outer) == 1 && outerCount == 1 {
		// This section handles "old style" multipolygons that don't/shouldn't
		// exist anymore. In the past tags were set on the outer ring way and
		// the relation was used to add holes to the way.
		outerRing := multiSegment(outer).Ring(orb.CCW)
		if len(outerRing) < 4 || !outerRing.Closed() {
			// not a valid outer ring
			return nil, errors.New("not a valid outer ring")
		}

		innerSections := join(inner)
		polygon := make(orb.Polygon, 0, len(inner)+1)

		polygon = append(polygon, outerRing)
		for _, is := range innerSections {
			polygon = append(polygon, is.Ring(orb.CW))
		}

		return orb.MultiPolygon{polygon}, nil
	} else {
		// more than one outer, need to map inner polygons to
		// the outer that contains them.
		outerSections := join(outer)

		mp := make(orb.MultiPolygon, 0, len(outer))
		for _, os := range outerSections {
			ring := os.Ring(orb.CCW)
			if len(ring) < 4 || !ring.Closed() {
				// needs at least 4 points and matching endpoints
				continue
			}

			mp = append(mp, orb.Polygon{ring})
		}

		if len(mp) == 0 {
			// no valid outer ways.
			return nil, errors.New("no valid outer ways")
		}

		innerSections := join(inner)
		for _, is := range innerSections {
			ring := is.Ring(orb.CW)
			mp = addToMultiPolygon(mp, ring)
		}

		if len(mp) == 0 {
			return orb.MultiPolygon{}, nil
		}

		return mp, nil
	}
}

func addToMultiPolygon(mp orb.MultiPolygon, ring orb.Ring) orb.MultiPolygon {
	for i := range mp {
		if polygonContains(mp[i][0], ring) {
			mp[i] = append(mp[i], ring)
			return mp
		}
	}

	if len(mp) > 0 {
		// if the outer ring of the first polygon is not closed,
		// we don't really know if this inner should be part of it.
		// But... we assume yes.
		fr := mp[0][0]
		if len(fr) != 0 && fr[0] != fr[len(fr)-1] {
			mp[0] = append(mp[0], ring)
			return mp
		}

		// trying to find an existing "without outer" polygon to add this to.
		for i := range mp {
			if len(mp[i][0]) == 0 {
				mp[i] = append(mp[i], ring)
				return mp
			}
		}
	}

	// no polygons with empty outer, so create one.
	// create another polygon with empty outer.
	return append(mp, orb.Polygon{nil, ring})
}

func polygonContains(outer orb.Ring, r orb.Ring) bool {
	for _, p := range r {
		inside := false

		x, y := p[0], p[1]
		i, j := 0, len(outer)-1
		for i < len(outer) {
			xi, yi := outer[i][0], outer[i][1]
			xj, yj := outer[j][0], outer[j][1]

			if ((yi > y) != (yj > y)) &&
				(x < (xj-xi)*(y-yi)/(yj-yi)+xi) {
				inside = !inside
			}

			j = i
			i++
		}

		if inside {
			return true
		}
	}

	return false
}

func reorient(p orb.Polygon) {
	if p[0].Orientation() != orb.CCW {
		p[0].Reverse()
	}

	for i := 1; i < len(p); i++ {
		if p[i].Orientation() != orb.CW {
			p[i].Reverse()
		}
	}
}

func join(segments []segment) []multiSegment {
	lists := []multiSegment{}
	segments = compact(segments)

	// matches are removed from `segments` and put into the current
	// group, so when `segments` is empty we're done.
	for len(segments) != 0 {
		current := multiSegment{segments[len(segments)-1]}
		segments = segments[:len(segments)-1]

		// if the current group is a ring, we're done.
		// else add in all the lines.
		for len(segments) != 0 && !current.First().Equal(current.Last()) {
			first := current.First()
			last := current.Last()

			foundAt := -1
			for i, segment := range segments {
				if last.Equal(segment.First()) {
					// nice fit at the end of current

					segment.Line = segment.Line[1:]
					current = append(current, segment)
					foundAt = i
					break
				} else if last.Equal(segment.Last()) {
					// reverse it and it'll fit at the end
					segment.Reverse()

					segment.Line = segment.Line[1:]
					current = append(current, segment)
					foundAt = i
					break
				} else if first.Equal(segment.Last()) {
					// nice fit at the start of current
					segment.Line = segment.Line[:len(segment.Line)-1]
					current = append(multiSegment{segment}, current...)

					foundAt = i
					break
				} else if first.Equal(segment.First()) {
					// reverse it and it'll fit at the start
					segment.Reverse()

					segment.Line = segment.Line[:len(segment.Line)-1]
					current = append(multiSegment{segment}, current...)

					foundAt = i
					break
				}
			}

			if foundAt == -1 {
				break // Invalid geometry (dangling way, unclosed ring)
			}

			// remove the found/matched segment from the list.
			if foundAt < len(segments)/2 {
				// first half, shift up
				for i := foundAt; i > 0; i-- {
					segments[i] = segments[i-1]
				}
				segments = segments[1:]
			} else {
				// second half, shift down
				for i := foundAt + 1; i < len(segments); i++ {
					segments[i-1] = segments[i]
				}
				segments = segments[:len(segments)-1]
			}
		}

		lists = append(lists, current)
	}

	return lists
}

func compact(ms multiSegment) multiSegment {
	at := 0
	for _, s := range ms {
		if len(s.Line) <= 1 {
			continue
		}

		ms[at] = s
		at++
	}

	return ms[:at]
}

// multiSegment is an ordered set of segments that form a continuous
// section of a multipolygon.
type multiSegment []segment

// First returns the first point in the list of linestrings.
func (ms multiSegment) First() orb.Point {
	return ms[0].Line[0]
}

// Last returns the last point in the list of linestrings.
func (ms multiSegment) Last() orb.Point {
	line := ms[len(ms)-1].Line
	return line[len(line)-1]
}

// LineString converts a multisegment into a geo linestring object.
func (ms multiSegment) LineString() orb.LineString {
	length := 0
	for _, s := range ms {
		length += len(s.Line)
	}

	line := make(orb.LineString, 0, length)
	for _, s := range ms {
		line = append(line, s.Line...)
	}

	return line
}

// Ring converts the multisegment to a ring of the given orientation.
// It uses the orientation on the members if possible.
func (ms multiSegment) Ring(o orb.Orientation) orb.Ring {
	length := 0
	for _, s := range ms {
		length += len(s.Line)
	}

	ring := make(orb.Ring, 0, length)

	haveOrient := false
	reversed := false
	for _, s := range ms {
		if s.Orientation != 0 {
			haveOrient = true

			// if s.Orientation == o && s.Reversed {
			// 	reversed = true
			// }
			// if s.Orientation != 0 && !s.Reversed {
			// 	reversed = true
			// }

			if (s.Orientation == o) == s.Reversed {
				reversed = true
			}
		}

		ring = append(ring, s.Line...)
	}

	if (haveOrient && reversed) || (!haveOrient && ring.Orientation() != o) {
		ring.Reverse()
	}

	return ring
}

// Orientation computes the orientation of a multisegment like if it was ring.
func (ms multiSegment) Orientation() orb.Orientation {
	area := 0.0
	prev := ms.First()

	// implicitly move everything to near the origin to help with roundoff
	offset := prev
	for _, segment := range ms {
		for _, point := range segment.Line {
			area += (prev[0]-offset[0])*(point[1]-offset[1]) -
				(point[0]-offset[0])*(prev[1]-offset[1])

			prev = point
		}
	}

	if area > 0 {
		return orb.CCW
	}

	return orb.CW
}

type segment struct {
	Index       uint32
	Orientation orb.Orientation
	Reversed    bool
	Line        orb.LineString
}

// Reverse will reverse the line string of the segment.
func (s *segment) Reverse() {
	s.Reversed = !s.Reversed
	s.Line.Reverse()
}

// First returns the first point in the segment linestring.
func (s segment) First() orb.Point {
	return s.Line[0]
}

// Last returns the last point in the segment linestring.
func (s segment) Last() orb.Point {
	return s.Line[len(s.Line)-1]
}
