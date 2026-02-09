package geoparser

import (
	"iter"
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

func (f *GeoGen) parseObject(o osm.Object) iter.Seq[geoPoint] {
	return func(yield func(geoPoint) bool) {
		switch obj := o.(type) {
		case *osm.Node:
			if point, ok := f.parseNode(obj); ok {
				if !yield(point) {
					return
				}
			}
		case *osm.Way:
			if !yieldConsume(f.parseWay(obj), yield) {
				return
			}
		case *osm.Relation:
			if !yieldConsume(f.parseRelation(obj), yield) {
				return
			}
		}
	}

}

type geoPoint struct {
	orb.Point
	geomodel.Info
}

const (
	weightAreaAdministrative uint8 = 1
	weightAreaProtected      uint8 = 2
	weightAreaIndustrial     uint8 = 3
	weightRoad               uint8 = 5
	weightBuilding           uint8 = 16
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

func (f *GeoGen) parseWay(way *osm.Way) iter.Seq[geoPoint] {
	if !f.parsedWays.AddIfAbsent(way.ID) {
		return nil
	}

	if isBuilding(way.Tags) {
		return func(yield func(geoPoint) bool) {
			for _, p := range f.parseWayBuilding(way) {
				if !yield(p) {
					break
				}
			}
		}
	} else if slices.Contains([]string{"motorway", "trunk", "primary", "secondary", "tertiary"}, way.Tags.Find("highway")) {
		return f.parseWayHighway(way)
	}

	return nil
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

func (f *GeoGen) parseWayHighway(way *osm.Way) iter.Seq[geoPoint] {
	ls := f.makeLineString(way.Nodes)
	ls = resample.ToInterval(ls, geo.Distance, f.config.HighwayPointsDistance)

	if len(ls) == 0 {
		return nil
	}

	return func(yield func(geoPoint) bool) {
		for _, point := range ls {
			name := f.getHighwayName(way.Tags)
			street := f.localizedStreetName(way.Tags)
			if street == "" {
				street = name
				name = ""
			}

			if !yield(geoPoint{
				Point: point,
				Info: geomodel.Info{
					Weight: weightRoad,
					Name:   name,
					Street: street,
					City:   f.localizedCityAddr(way.Tags, point),
					Region: f.localizedRegion(point),
				},
			}) {
				return
			}
		}
	}

}

func (f *GeoGen) parseRelation(rel *osm.Relation) iter.Seq[geoPoint] {
	if !f.parsedRelations.AddIfAbsent(rel.ID) {
		return nil
	}

	switch rel.Tags.Find("type") {
	case "multipolygon", "boundary":
		switch rel.Tags.Find("landuse") {
		case "quarry", "industrial":
			return f.parseRelationArea(rel, weightAreaIndustrial)
		}
		if rel.Tags.Find("boundary") == "protected_area" {
			return f.parseRelationArea(rel, weightAreaProtected)
		}
		if isBuilding(rel.Tags) {
			return f.parseRelationBuilding(rel)
		}
		if rel.Tags.Find("boundary") == "administrative" {
			if rel.Tags.Find("admin_level") == "4" {
				return f.parseRelationArea(rel, weightAreaAdministrative)
			}
		}
	case "building":
		if rel.Tags.Find("route") == "road" && strings.Contains(rel.Tags.Find("network"), "national") {
			return f.parseRelationHighway(rel)
		}
	}

	return nil
}

func (f *GeoGen) parseRelationBuilding(rel *osm.Relation) iter.Seq[geoPoint] {
	return func(yield func(geoPoint) bool) {
		if rel.Tags.Find("type") == "multipolygon" {
			mpoly, err := f.buildPolygon(rel.Members)
			if err != nil {
				slog.Error("Error building polygon", "error", err.Error())
				return
			}
			if mpoly == nil && len(mpoly) == 0 {
				slog.Error("Empty polygon", "name", rel.Tags.Find("name"))
				return
			}

			for _, poly := range mpoly {
				p, _ := planar.CentroidArea(poly)

				if !yield(geoPoint{
					Point: p,
					Info: geomodel.Info{
						Weight:      weightBuilding,
						Name:        f.localizedName(rel.Tags),
						Street:      f.localizedStreetName(rel.Tags),
						HouseNumber: rel.Tags.Find("addr:housenumber"),
						City:        f.localizedCityAddr(rel.Tags, p),
						Region:      f.localizedRegion(p),
					},
				}) {
					return
				}
			}
		}
	}
}

func (f *GeoGen) parseRelationHighway(rel *osm.Relation) iter.Seq[geoPoint] {
	return func(yield func(geoPoint) bool) {
		for _, m := range rel.Members {
			if m.Type != osm.TypeWay {
				continue
			}

			way, err := f.osmdb.GetWay(osm.WayID(m.Ref))
			if err != nil {
				f.log.Error("Error getting way", "id", m.Ref, "error", err.Error())
				continue
			}

			if !yieldConsume(f.parseWay(way), yield) {
				return
			}
		}
	}
}

func (f *GeoGen) parseRelationArea(rel *osm.Relation, weight uint8) iter.Seq[geoPoint] {
	log := f.log.With("type", "relation", "id", rel.ID)

	return func(yield func(geoPoint) bool) {
		name := f.localizedName(rel.Tags)
		if name == "" {
			return
		}

		poly, err := f.buildPolygon(rel.Members)
		if err != nil {
			log.Error("Error building polygon", "error", err.Error())
			return
		}

		for p := range fillPolygonWithPoints(poly, f.config.RegionPointsAngleDistance) {
			point := geoPoint{
				Point: p,
				Info: geomodel.Info{
					Weight:      weight,
					Name:        name,
					Street:      "",
					HouseNumber: "",
					City:        f.localizedCityAddr(rel.Tags, p),
					Region:      f.localizedRegion(p),
				},
			}
			if !yield(point) {
				return
			}
		}
	}

}

func yieldConsume[V any](it iter.Seq[V], yield func(V) bool) bool {
	if it == nil {
		return true
	}

	for v := range it {
		if !yield(v) {
			return false
		}
	}

	return true
}
