package geoparser

import (
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/osm"
)

// FIXME it takes ALOT of time, optimize
func (f *GeoGen) calcPlace(point orb.Point) cachePlace {
	var foundPlace cachePlace
	f.placeCache.Range(func(_ osm.RelationID, place cachePlace) bool {
		if place.Bound.Contains(point) {
			if planar.MultiPolygonContains(place.MultiPolygon, point) {
				foundPlace = place
				return false
			}
		}

		return true
	})
	return foundPlace
}

func (f *GeoGen) calcWayCenter(way *osm.Way) (lat, lon float64) {
	poly := orb.Ring(f.makeLineString(way.Nodes))

	if len(poly) == 0 {
		return 0, 0
	}

	p, _ := planar.CentroidArea(poly)
	return p[0], p[1]
}
