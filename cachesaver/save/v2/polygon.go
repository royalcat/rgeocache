package savev2

import (
	"github.com/paulmach/orb"
	savev2proto "github.com/royalcat/rgeocache/cachesaver/save/v2/proto"
)

// --- v2 proto conversions (used by save/load) ---

func mapBoundsToV2(bounds orb.Bound) *savev2proto.Bounds {
	return &savev2proto.Bounds{
		Max: &savev2proto.LatLon{
			Lat: float32(bounds.Max.Lat()),
			Lon: float32(bounds.Max.Lon()),
		},
		Min: &savev2proto.LatLon{
			Lat: float32(bounds.Min.Lat()),
			Lon: float32(bounds.Min.Lon()),
		},
	}
}

func mapBoundsFromV2(bounds *savev2proto.Bounds) orb.Bound {
	if bounds == nil {
		return orb.Bound{}
	}
	return orb.Bound{
		Max: orb.Point{float64(bounds.Max.Lon), float64(bounds.Max.Lat)},
		Min: orb.Point{float64(bounds.Min.Lon), float64(bounds.Min.Lat)},
	}
}

func mapMultiPolygonToV2(mpolygon orb.MultiPolygon) *savev2proto.MultiPolygon {
	polygons := make([]*savev2proto.Polygon, 0, len(mpolygon))
	for _, poly := range mpolygon {
		rings := make([]*savev2proto.Ring, 0, len(poly))
		for _, ring := range poly {
			points := make([]*savev2proto.LatLon, len(ring))
			for i, point := range ring {
				points[i] = &savev2proto.LatLon{
					Lat: float32(point[1]),
					Lon: float32(point[0]),
				}
			}
			rings = append(rings, &savev2proto.Ring{Points: points})
		}
		polygons = append(polygons, &savev2proto.Polygon{Rings: rings})
	}
	return &savev2proto.MultiPolygon{Polygons: polygons}
}

func mapMultiPolygonFromV2(mpolygon *savev2proto.MultiPolygon) orb.MultiPolygon {
	if mpolygon == nil {
		return nil
	}
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
