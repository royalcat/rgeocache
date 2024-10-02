package geoparser

import (
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/osm"
)

// FIXME it takes ALOT of time, optimize
func (f *GeoGen) calcPlace(point orb.Point) cachePlace {
	var foundPlace cachePlace
	f.placeCache.Range(func(_ int64, place cachePlace) bool {
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
	poly := orb.Ring{}

	line, ok := f.wayCache.Get(int64(way.ID))
	if ok {
		return 0, 0
	}

	for _, p := range line {
		poly = append(poly, p)
	}

	// for _, node := range way.Nodes {

	// 	if node.Lat != 0 && node.Lon != 0 {
	// 		poly = append(poly, orb.Point{node.Lat, node.Lon})
	// 	} else {
	// 		if p, ok := f.nodeCache.Get(int64(node.ID)); ok && p[0] != 0 && p[1] != 0 {
	// 			poly = append(poly, orb.Point{p[0], p[1]})
	// 		}

	// 	}
	// }

	if len(poly) == 0 {
		return 0, 0
	}

	//return Polylabel(poly, 0.00001)
	p, _ := planar.CentroidArea(poly)
	return p[0], p[1]
}
