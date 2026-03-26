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
	"github.com/paulmach/orb/simplify"
	"github.com/paulmach/osm"
)

func (f *GeoGen) parseObject(o osm.Object) {
	switch obj := o.(type) {
	case *osm.Node:
		if point, ok := f.parseNode(obj); ok {
			f.parsedPoints <- point
		}
	case *osm.Way:
		for _, point := range f.parseWay(obj) {
			f.parsedPoints <- point
		}
	case *osm.Relation:
		for _, point := range f.parseRelation(obj) {
			f.parsedPoints <- point
		}
	}
}

type geoPoint struct {
	orb.Point
	geomodel.Info
}

const (
	weightBuilding       = 10
	weightRoad           = 5
	weightAreaIndustrial = 3
	weightAreaProtected  = 2
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
	if !f.parsedWays.SetIfAbsent(way.ID, struct{}{}) {
		f.parsedWaysDupes.Add(1)
		return []geoPoint{}
	}

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
		name := f.getHighwayName(way.Tags)
		street := f.localizedStreetName(way.Tags)
		if street == "" {
			street = name
			name = ""
		}

		out = append(out, geoPoint{
			Point: point,
			Info: geomodel.Info{
				Weight: weightRoad,
				Name:   name,
				Street: street,
				City:   f.localizedCityAddr(way.Tags, point),
				Region: f.localizedRegion(point),
			},
		})
	}
	return out
}

func (f *GeoGen) parseRelation(rel *osm.Relation) []geoPoint {
	if !f.parsedRelations.SetIfAbsent(rel.ID, struct{}{}) {
		f.parsedRelationsDupes.Add(1)
		return []geoPoint{}
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
			switch rel.Tags.Find("admin_level") {
			case "4":
				f.parseRelationRegion(rel)
				return []geoPoint{}
			case "2":
				f.parseRelationCountry(rel)
				return []geoPoint{}
			}

		}
	case "building":
		if rel.Tags.Find("route") == "road" && strings.Contains(rel.Tags.Find("network"), "national") {
			return f.parseRelationHighway(rel)
		}
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

		way, err := f.osmdb.GetWay(osm.WayID(m.Ref))
		if err != nil {
			f.log.Error("Error getting way", "id", m.Ref, "error", err.Error())
			continue
		}

		out = append(out, f.parseWay(way)...)
	}

	return out
}

func (f *GeoGen) parseRelationArea(rel *osm.Relation, weight uint8) []geoPoint {
	log := f.log.With("type", "relation", "id", rel.ID)

	name := f.localizedName(rel.Tags)
	if name == "" {
		return []geoPoint{}
	}

	poly, err := f.buildPolygon(rel.Members)
	if err != nil {
		log.Error("Error building polygon", "error", err.Error())
		return []geoPoint{}
	}

	points := fillPolygonWithPoints(poly, 0.01/2)

	out := make([]geoPoint, 0, len(points))
	for _, p := range points {
		out = append(out, geoPoint{
			Point: p,
			Info: geomodel.Info{
				Weight:      weight,
				Name:        name,
				Street:      "",
				HouseNumber: "",
				City:        f.localizedCityAddr(rel.Tags, p),
				Region:      f.localizedRegion(p),
			},
		})
	}
	return out
}

func (f *GeoGen) parseRelationRegion(rel *osm.Relation) {
	log := f.log.With("func", "parseRelationRegion", "type", "relation", "id", rel.ID)
	name := f.localizedName(rel.Tags)
	if name == "" {
		return
	}

	poly, err := f.buildPolygon(rel.Members)
	if err != nil {
		log.Error("Error building polygon", "error", err.Error())
		return
	}

	poly = simplify.DouglasPeucker(0.01).MultiPolygon(poly)

	f.regionsMu.Lock()
	defer f.regionsMu.Unlock()

	f.regions = append(f.regions, geomodel.Zone{
		Name:    name,
		Bounds:  poly.Bound(),
		Polygon: poly,
	})
}

func (f *GeoGen) parseRelationCountry(rel *osm.Relation) {
	log := f.log.With("func", "parseRelationCountry", "type", "relation", "id", rel.ID)
	name := f.localizedName(rel.Tags)
	if name == "" {
		return
	}

	poly, err := f.buildPolygon(rel.Members)
	if err != nil {
		log.Error("Error building polygon", "error", err.Error())
		return
	}

	poly = simplify.DouglasPeucker(0.01).MultiPolygon(poly)

	f.countriesMu.Lock()
	defer f.countriesMu.Unlock()

	f.countries = append(f.countries, geomodel.Zone{
		Name:    name,
		Bounds:  poly.Bound(),
		Polygon: poly,
	})
}
