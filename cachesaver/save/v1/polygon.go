package savev1

import (
	"github.com/paulmach/orb"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
)

func mapBoundsFromOrb(bounds orb.Bound) *saveproto.Bounds {
	return &saveproto.Bounds{
		Max: &saveproto.LatLon{
			Lat: float32(bounds.Max.Lat()),
			Lon: float32(bounds.Max.Lon()),
		},
		Min: &saveproto.LatLon{
			Lat: float32(bounds.Min.Lat()),
			Lon: float32(bounds.Min.Lon()),
		},
	}
}

func mapBoundsToOrb(bounds *saveproto.Bounds) orb.Bound {
	return orb.Bound{
		Max: orb.Point{float64(bounds.Max.Lon), float64(bounds.Max.Lat)},
		Min: orb.Point{float64(bounds.Min.Lon), float64(bounds.Min.Lat)},
	}
}

func mapMultiPolygonFromOrb(mpolygon orb.MultiPolygon) *saveproto.MultiPolygon {
	polygons := make([]*saveproto.Polygon, 0, len(mpolygon))
	for _, poly := range mpolygon {
		rings := make([]*saveproto.Ring, 0, len(poly))
		for _, ring := range poly {
			points := make([]*saveproto.LatLon, len(ring))
			for i, point := range ring {
				points[i] = &saveproto.LatLon{
					Lat: float32(point[1]),
					Lon: float32(point[0]),
				}
			}
			rings = append(rings, &saveproto.Ring{
				Points: points,
			})
		}
		polygons = append(polygons, &saveproto.Polygon{
			Rings: rings,
		})
	}

	return &saveproto.MultiPolygon{
		Polygons: polygons,
	}
}

func mapMultiPolygonToOrb(mpolygon *saveproto.MultiPolygon) orb.MultiPolygon {
	polygons := make(orb.MultiPolygon, 0, len(mpolygon.Polygons))
	for _, poly := range mpolygon.Polygons {
		rings := make([]orb.Ring, 0, len(poly.Rings))
		for _, ring := range poly.Rings {
			points := make([]orb.Point, 0, len(ring.Points))
			for _, point := range ring.Points {
				points = append(points, orb.Point{float64(point.Lon), float64(point.Lat)})
			}
			rings = append(rings, orb.Ring(points))
		}
		polygons = append(polygons, orb.Polygon(rings))
	}

	return orb.MultiPolygon(polygons)
}
