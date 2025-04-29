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
	out, _ := f.regionIndex.QueryPoint(point)
	return out
}

func (f *GeoGen) calcWayCenter(way *osm.Way) orb.Point {
	poly := orb.Ring(f.makeLineString(way.Nodes))

	if len(poly) == 0 {
		return orb.Point{}
	}

	p, _ := planar.CentroidArea(poly)
	return p
}
