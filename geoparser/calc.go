package geoparser

import (
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/osm"
)

// FIXME it takes ALOT of time, optimize
func (f *GeoGen) calcCity(lat, lon float64) string {
	cityName := ""
	point := orb.Point{lat, lon}
	f.cityCache.Range(func(_ int64, city cacheCity) bool {
		if city.Bound.Contains(point) {
			if planar.MultiPolygonContains(city.MultiPolygon, point) {
				cityName = city.Name
				return false
			}
		}

		return true
	})
	return cityName
}

func (f *GeoGen) calcWayCenter(way *osm.Way) (lat, lon float64) {
	poly := orb.Ring{}
	for _, node := range way.Nodes {
		if node.Lat != 0 && node.Lon != 0 {
			poly = append(poly, orb.Point{node.Lat, node.Lon})
		} else {
			if p, ok := f.nodeCache.Get(int64(node.ID)); ok && p[0] != 0 && p[1] != 0 {
				poly = append(poly, orb.Point{p[0], p[1]})
			}

		}
	}

	if len(poly) == 0 {
		return 0, 0
	}

	//return Polylabel(poly, 0.00001)
	p, _ := planar.CentroidArea(poly)
	return p[0], p[1]
}
