package geoparser

import (
	"context"
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

func (f *GeoGen) parseObject(ctx context.Context, o osm.Object) {
	ctx, span := tracer.Start(ctx, "parseObject")
	defer span.End()

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

const (
	weightBuilding = 10
	weightRoad     = 5
	weightArea     = 3
)

func isBuilding(tags osm.Tags) bool {
	return tags.HasTag("addr:housenumber") && tags.HasTag("addr:street") && tags.HasTag("building")
}

func (f *GeoGen) parseNode(node *osm.Node) (geoPoint, bool) {
	if isBuilding(node.Tags) {
		point := orb.Point{node.Lon, node.Lat}

		return geoPoint{
			Point: point,
			Info: geomodel.Info{
				Weight:      weightBuilding,
				Name:        f.localizedName(node.Tags),
				Street:      f.localizedStreetName(node.Tags),
				HouseNumber: node.Tags.Find("addr:housenumber"),
				City:        f.localizedCityAddr(node.Tags, point),
				Region:      f.localizedRegion(point),
			},
		}, true
	}

	return geoPoint{}, false
}

func (f *GeoGen) parseWay(way *osm.Way) []geoPoint {
	if isBuilding(way.Tags) {
		return f.parseWayBuilding(way)
	} else if slices.Contains([]string{"motorway", "trunk", "primary", "secondary", "tertiary"}, way.Tags.Find("highway")) {
		return f.parseWayHighway(way)
	}

	return []geoPoint{}
}

func (f *GeoGen) parseWayBuilding(way *osm.Way) []geoPoint {
	log := f.log.With("type", "way", "id", way.ID)

	point := f.calcWayCenter(way)

	if point.X() == 0 && point.Y() == 0 {
		log.Warn("failed to calculate center for way")
		return []geoPoint{}
	}

	return []geoPoint{{
		Point: point,
		Info: geomodel.Info{
			Weight:      weightBuilding,
			Name:        f.localizedName(way.Tags),
			Street:      f.localizedStreetName(way.Tags),
			HouseNumber: way.Tags.Find("addr:housenumber"),
			City:        f.localizedCityAddr(way.Tags, point),
			Region:      f.localizedRegion(point),
		},
	}}
}

func (f *GeoGen) parseWayHighway(way *osm.Way) []geoPoint {
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
				Weight: weightRoad,
				Name:   f.getHighwayName(way.Tags),
				Street: f.localizedStreetName(way.Tags),
				City:   f.localizedCityAddr(way.Tags, point),
				Region: f.localizedRegion(point),
			},
		})
	}
	return out
}

func (f *GeoGen) parseRelation(rel *osm.Relation) []geoPoint {
	if isBuilding(rel.Tags) {
		return f.parseRelationBuilding(rel)
	} else if rel.Tags.Find("route") == "road" && rel.Tags.Find("type") == "route" && strings.Contains(rel.Tags.Find("network"), "national") {
		return f.parseRelationHighway(rel)
	} else if rel.Tags.Find("boundary") == "protected_area" && rel.Tags.Find("type") == "boundary" {
		return f.parseRelationArea(rel)
	}

	return []geoPoint{}
}

func (f *GeoGen) parseRelationBuilding(rel *osm.Relation) []geoPoint {
	points := []geoPoint{}

	if rel.Tags.Find("type") == "multipolygon" {
		mpoly, err := f.buildPolygon(rel.Members)
		if err != nil {
			slog.Error("Error building polygon", "error", err.Error())
			return points
		}
		if mpoly == nil && len(mpoly) == 0 {
			slog.Error("Empty polygon", "name", rel.Tags.Find("name"))
			return points
		}

		for _, poly := range mpoly {
			p, _ := planar.CentroidArea(poly)

			points = append(points, geoPoint{
				Point: p,
				Info: geomodel.Info{
					Weight:      weightBuilding,
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

	out := []geoPoint{}
	for _, m := range rel.Members {
		if m.Type != osm.TypeWay {
			continue
		}

		if f.parsedWays.ContainsOrAdd(osm.WayID(m.Ref)) {
			continue
		}

		way, err := f.osmdb.GetWay(osm.WayID(m.Ref))
		if err != nil {
			f.log.Error("Error getting way", "id", m.Ref, "error", err.Error())
			continue
		}

		out = append(out, f.parseWayHighway(way)...)
	}

	return out
}

func (f *GeoGen) parseRelationArea(rel *osm.Relation) []geoPoint {
	log := f.log.With("type", "relation", "id", rel.ID)

	poly, err := f.buildPolygon(rel.Members)
	if err != nil {
		log.Error("Error building polygon", "error", err.Error())
		return []geoPoint{}
	}

	points := fillPolygonWithPoints(poly, 0.01)

	out := []geoPoint{}
	for _, p := range points {
		out = append(out, geoPoint{
			Point: p,
			Info: geomodel.Info{
				Weight:      weightArea,
				Name:        f.localizedName(rel.Tags),
				Street:      f.localizedStreetName(rel.Tags),
				HouseNumber: rel.Tags.Find("addr:housenumber"),
				City:        f.localizedCityAddr(rel.Tags, p),
				Region:      f.localizedRegion(p),
			},
		})
	}
	return out
}
