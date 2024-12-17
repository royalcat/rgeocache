package geoparser

import (
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/osm"
)

func (f *GeoGen) calcPlace(point orb.Point) string {
	out, _ := f.placeIndex.QueryPoint(point)
	return out
}

func (f *GeoGen) calcRegion(point orb.Point) string {
	out, _ := f.regoinIndex.QueryPoint(point)
	return out
}

func (f *GeoGen) calcWayCenter(way *osm.Way) (lat, lon float64) {
	poly := orb.Ring(f.makeLineString(way.Nodes))

	if len(poly) == 0 {
		return 0, 0
	}

	p, _ := planar.CentroidArea(poly)
	return p[0], p[1]
}
