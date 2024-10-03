package geoparser

import (
	"strings"

	"github.com/royalcat/rgeocache/geomodel"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/osm"
	"github.com/sirupsen/logrus"
)

func (f *GeoGen) parseObject(o osm.Object) {
	switch obj := o.(type) {
	case *osm.Node:

		if point, ok := f.parseNode(obj); ok {
			f.points.Set(point.Point, point.Info)
		}
	case *osm.Way:
		if point, ok := f.parseWay(obj); ok {
			f.points.Set(point.Point, point.Info)
		}
	case *osm.Relation:
		for _, point := range f.parseRelation(obj) {
			f.points.Set(point.Point, point.Info)
		}
	}
}

type geoPoint struct {
	orb.Point
	geomodel.Info
}

func (f *GeoGen) parseNode(node *osm.Node) (geoPoint, bool) {
	street := node.Tags.Find("addr:street")
	housenumber := node.Tags.Find("addr:housenumber")
	building := node.Tags.Find("building")

	if housenumber != "" && street != "" && building != "" {
		return geoPoint{
			Point: orb.Point{node.Lat, node.Lon},
			Info: geomodel.Info{
				Name:        f.localizedName(node.Tags),
				Street:      f.localizedStreetName(node.Tags),
				HouseNumber: housenumber,
				City:        f.localizeCityAddr(node.Tags, orb.Point{node.Lat, node.Lon}),
			},
		}, true
	}

	return geoPoint{}, false
}

func (f *GeoGen) parseWay(way *osm.Way) (geoPoint, bool) {
	log := logrus.WithField("id", way.ID)

	street := way.Tags.Find("addr:street")
	housenumber := way.Tags.Find("addr:housenumber")
	building := way.Tags.Find("building")

	if housenumber != "" && street != "" && building != "" {
		lat, lon := calcWayCenter(f.wayCache, way)

		if lat == 0 && lon == 0 {
			log.Warn("failed to calculate center for way")
			return geoPoint{}, false
		}

		return geoPoint{
			Point: orb.Point{lat, lon},
			Info: geomodel.Info{
				Name:        f.localizedName(way.Tags),
				Street:      f.localizedStreetName(way.Tags),
				HouseNumber: housenumber,
				City:        f.localizeCityAddr(way.Tags, orb.Point{lat, lon}),
			},
		}, true
	}

	return geoPoint{}, false
}

func (f *GeoGen) parseRelation(rel *osm.Relation) []geoPoint {
	points := []geoPoint{}

	tags := rel.TagMap()
	street := tags["addr:street"]
	housenumber := tags["addr:housenumber"]
	building := tags["building"]

	if housenumber != "" && street != "" && building != "" {
		return f.parseRelationBuilding(rel)
	}

	if tags["route"] == "road" && tags["type"] == "route" && strings.Contains(tags["network"], "national") {
		return f.parseRelationHighway(rel)
	}

	return points
}

func (f *GeoGen) parseRelationBuilding(rel *osm.Relation) []geoPoint {
	points := []geoPoint{}
	tags := rel.TagMap()

	if tags["type"] == "multipolygon" {
		mpoly, err := buildPolygon(f.wayCache, rel.Members)
		if err != nil {
			logrus.Errorf("Error building polygon: %s", err.Error())
			return points
		}
		if mpoly == nil && len(mpoly) == 0 {
			logrus.Errorf("Empty polygon: %s", tags["name"])
			return points
		}

		for _, poly := range mpoly {
			p, _ := planar.CentroidArea(poly)

			points = append(points, geoPoint{
				Point: p,
				Info: geomodel.Info{
					Name:        f.localizedName(rel.Tags),
					Street:      f.localizedStreetName(rel.Tags),
					HouseNumber: rel.Tags.Find("addr:housenumber"),
					City:        f.localizeCityAddr(rel.Tags, p),
				},
			})
		}
	}

	return points
}

func (f *GeoGen) parseRelationHighway(rel *osm.Relation) []geoPoint {
	points := []geoPoint{}

	for _, m := range rel.Members {
		if m.Type != osm.TypeWay {
			continue
		}

		if way, ok := f.wayCache.Get(osm.WayID(m.Ref)); ok {
			for _, point := range orb.LineString(way) {

				points = append(points, geoPoint{
					Point: point,
					Info: geomodel.Info{
						Name:   f.localizedName(rel.Tags),
						Street: f.localizedStreetName(rel.Tags),
						City:   f.localizeCityAddr(rel.Tags, point),
					},
				})
			}
		}
	}
	return points
}
