package geoparser

import (
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/osm"
	"github.com/royalcat/rgeocache/kv"
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

func calcWayCenter(wayCache kv.KVS[osm.WayID, cacheWay], way *osm.Way) (lat, lon float64) {
	poly := orb.Ring{}

	line, ok := wayCache.Get(way.ID)
	if !ok {
		return 0, 0
	}

	for _, p := range line {
		poly = append(poly, p)
	}

	if len(poly) == 0 {
		return 0, 0
	}

	//return Polylabel(poly, 0.00001)
	p, _ := planar.CentroidArea(poly)
	return p[0], p[1]
}
