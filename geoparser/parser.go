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
			f.parsedPointsMu.Lock()
			defer f.parsedPointsMu.Unlock()
			f.parsedPoints = append(f.parsedPoints, point)
		}
	case *osm.Way:
		if point, ok := f.parseWay(obj); ok {
			f.parsedPointsMu.Lock()
			defer f.parsedPointsMu.Unlock()
			f.parsedPoints = append(f.parsedPoints, point)
		}
	case *osm.Relation:
		rels := f.parseRelation(obj)

		f.parsedPointsMu.Lock()
		defer f.parsedPointsMu.Unlock()
		for _, point := range rels {
			f.parsedPoints = append(f.parsedPoints, point)
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
		point := orb.Point{node.Lon, node.Lat}

		return geoPoint{
			Point: point,
			Info: geomodel.Info{
				Name:        f.localizedName(node.Tags),
				Street:      f.localizedStreetName(node.Tags),
				HouseNumber: housenumber,
				City:        f.localizedCityAddr(node.Tags, point),
				Region:      f.localizedRegion(point),
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
		point := f.calcWayCenter(way)

		if point.X() == 0 && point.Y() == 0 {
			log.Warn("failed to calculate center for way")
			return geoPoint{}, false
		}

		return geoPoint{
			Point: point,
			Info: geomodel.Info{
				Name:        f.localizedName(way.Tags),
				Street:      f.localizedStreetName(way.Tags),
				HouseNumber: housenumber,
				City:        f.localizedCityAddr(way.Tags, point),
				Region:      f.localizedRegion(point),
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
		mpoly, err := f.buildPolygon(rel.Members)
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
					City:        f.localizedCityAddr(rel.Tags, p),
					Region:      f.localizedRegion(p),
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

		way, err := f.osmdb.GetWay(osm.WayID(m.Ref))
		if err != nil {
			f.log.Errorf("Error getting way id %d: %s", m.Ref, err.Error())
			continue
		}

		ls := f.makeLineString(way.Nodes)

		for _, point := range ls {
			points = append(points, geoPoint{
				Point: point,
				Info: geomodel.Info{
					Name:   f.localizedName(rel.Tags),
					Street: f.localizedStreetName(rel.Tags),
					City:   f.localizedCityAddr(rel.Tags, point),
					Region: f.localizedRegion(point),
				},
			})
		}

	}
	return points
}
