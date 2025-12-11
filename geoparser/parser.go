package geoparser

import (
	"log/slog"
	"slices"
	"strings"

	"github.com/royalcat/rgeocache/geomodel"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geo"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/orb/resample"
	"github.com/paulmach/osm"
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
		points := f.parseWay(obj)
		if len(points) > 0 {
			f.parsedPointsMu.Lock()
			defer f.parsedPointsMu.Unlock()
			f.parsedPoints = append(f.parsedPoints, points...)
		}
	case *osm.Relation:
		rels := f.parseRelation(obj)
		if len(rels) > 0 {
			f.parsedPointsMu.Lock()
			defer f.parsedPointsMu.Unlock()
			f.parsedPoints = append(f.parsedPoints, rels...)
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

func (f *GeoGen) parseWay(way *osm.Way) []geoPoint {
	if f.parsedWays.ContainsAndAdd(way.ID) {
		return []geoPoint{}
	}

	log := f.log.With("id", way.ID)

	street := way.Tags.Find("addr:street")
	housenumber := way.Tags.Find("addr:housenumber")
	building := way.Tags.Find("building")

	if housenumber != "" && street != "" && building != "" {
		point := f.calcWayCenter(way)

		if point.X() == 0 && point.Y() == 0 {
			log.Warn("failed to calculate center for way")
			return []geoPoint{}
		}

		return []geoPoint{{
			Point: point,
			Info: geomodel.Info{
				Name:        f.localizedName(way.Tags),
				Street:      f.localizedStreetName(way.Tags),
				HouseNumber: housenumber,
				City:        f.localizedCityAddr(way.Tags, point),
				Region:      f.localizedRegion(point),
			},
		}}
	}

	highwayTag := way.Tags.Find("highway")
	if slices.Contains([]string{"motorway", "trunk", "primary", "secondary", "tertiary"}, highwayTag) {
		return f.parseWayHighway(way)
	}

	return []geoPoint{}
}

func (f *GeoGen) parseRelation(rel *osm.Relation) []geoPoint {
	if f.parsedRelations.ContainsAndAdd(rel.ID) {
		return []geoPoint{}
	}

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

	return []geoPoint{}
}

func (f *GeoGen) parseRelationBuilding(rel *osm.Relation) []geoPoint {
	if f.parsedRelations.ContainsAndAdd(rel.ID) {
		return []geoPoint{}
	}

	points := []geoPoint{}
	tags := rel.TagMap()

	if tags["type"] == "multipolygon" {
		mpoly, err := f.buildPolygon(rel.Members)
		if err != nil {
			slog.Error("Error building polygon", "error", err.Error())
			return points
		}
		if mpoly == nil && len(mpoly) == 0 {
			slog.Error("Empty polygon", "name", tags["name"])
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

func (f *GeoGen) getHighwayName(tags osm.Tags) string {
	highwayName := f.localizedName(tags)
	ref := tags.Find("ref")

	name := strings.Join([]string{ref, highwayName}, " ")

	return name
}

func (f *GeoGen) parseRelationHighway(rel *osm.Relation) []geoPoint {
	if f.parsedRelations.ContainsAndAdd(rel.ID) {
		return []geoPoint{}
	}

	points := []geoPoint{}

	for _, m := range rel.Members {
		if m.Type != osm.TypeWay {
			continue
		}

		if f.parsedWays.ContainsAndAdd(osm.WayID(m.Ref)) {
			return []geoPoint{}
		}

		way, err := f.osmdb.GetWay(osm.WayID(m.Ref))
		if err != nil {
			f.log.Error("Error getting way", "id", m.Ref, "error", err.Error())
			continue
		}

		ls := f.makeLineString(way.Nodes)
		// ls = resample.ToInterval(ls, geo.Distance, f.config.HighwayPointsDistance)

		for _, point := range ls {
			points = append(points, geoPoint{
				Point: point,
				Info: geomodel.Info{
					Name:   f.getHighwayName(rel.Tags),
					Street: f.localizedStreetName(rel.Tags),
					City:   f.localizedCityAddr(rel.Tags, point),
					Region: f.localizedRegion(point),
				},
			})
		}

	}
	return points
}

func (f *GeoGen) parseWayHighway(way *osm.Way) []geoPoint {
	if f.parsedWays.ContainsAndAdd(way.ID) {
		return []geoPoint{}
	}

	ls := f.makeLineString(way.Nodes)
	ls = resample.ToInterval(ls, geo.Distance, f.config.HighwayPointsDistance)

	if len(ls) == 0 {
		return []geoPoint{}
	}

	out := make([]geoPoint, 0, len(ls))
	for _, point := range ls {
		out = append(out, geoPoint{
			Point: point,
			Info: geomodel.Info{
				Name:   f.getHighwayName(way.Tags),
				Street: f.localizedStreetName(way.Tags),
				City:   f.localizedCityAddr(way.Tags, point),
				Region: f.localizedRegion(point),
			},
		})
	}
	return out
}
