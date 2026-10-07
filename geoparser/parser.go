package geoparser

import (
	"log/slog"
	"strings"
	"unique"

	"github.com/fogleman/poissondisc"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/geomodel"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/orb/simplify"
	"github.com/paulmach/osm"
)

func (f *GeoGen) parseObject(o osm.Object) {
	switch obj := o.(type) {
	case *osm.Node:
		if point, ok := f.parseNode(obj); ok {
			f.parsedItems <- parseItem{Point: point}
		}
	case *osm.Way:
		for _, point := range f.parseWay(obj) {
			f.parsedItems <- parseItem{Point: point}
		}
	case *osm.Relation:
		for _, point := range f.parseRelation(obj) {
			f.parsedItems <- parseItem{Point: point}
		}
	}
}

// parseItem is one element of the parse output stream: either a cache point or
// a graph edge.
type parseItem struct {
	IsEdge bool
	Point  geoPoint
	Edge   cachemodel.GraphEdge
}

type geoPoint struct {
	orb.Point

	Name        string                `json:"name"`
	Street      unique.Handle[string] `json:"street"`
	HouseNumber unique.Handle[string] `json:"house_number"`
	City        unique.Handle[string] `json:"city"`
	Region      unique.Handle[string] `json:"region"`
	Country     unique.Handle[string] `json:"country"`

	Weight uint8                    `json:"weight"`
	Type   cachemodel.GeoObjectType `json:"type"`

	// GraphNode is the dense graph node id of this point (0 when the point is
	// not a road graph node).
	GraphNode uint32 `json:"graph_node"`
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
			Point:       point,
			Weight:      weightBuilding,
			Type:        cachemodel.GeoObjectBuilding,
			Name:        f.localizedName(node.Tags),
			Street:      f.localizedStreetName(node.Tags),
			HouseNumber: unique.Make(node.Tags.Find("addr:housenumber")),
			City:        f.localizedCityAddr(node.Tags, point),
			Region:      f.localizedRegion(point),
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
	} else if _, ok := highwayClasses[way.Tags.Find("highway")]; ok {
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
		Point:       point,
		Weight:      weightBuilding,
		Type:        cachemodel.GeoObjectBuilding,
		Name:        f.localizedName(way.Tags),
		Street:      f.localizedStreetName(way.Tags),
		HouseNumber: unique.Make(way.Tags.Find("addr:housenumber")),
		City:        f.localizedCityAddr(way.Tags, point),
		Region:      f.localizedRegion(point),
	}}
}

// highwayClasses maps the OSM highway values kept in the cache to the class
// byte stored on graph edges.
var highwayClasses = map[string]uint8{
	"motorway":  cachemodel.GraphClassMotorway,
	"trunk":     cachemodel.GraphClassTrunk,
	"primary":   cachemodel.GraphClassPrimary,
	"secondary": cachemodel.GraphClassSecondary,
	"tertiary":  cachemodel.GraphClassTertiary,
}

// parseWayHighway emits the way's OSM shape points as road points and links
// consecutive points into graph edges.
//
// Shape points replace the previous fixed-distance resampling: each OSM node
// becomes exactly one cache point (the first way to claim it emits it), so ways
// that share a junction reference the same point and the graph is routable.
func (f *GeoGen) parseWayHighway(way *osm.Way) []geoPoint {
	class, ok := highwayClasses[way.Tags.Find("highway")]
	if !ok {
		return []geoPoint{}
	}

	name := f.getHighwayName(way.Tags)
	street := f.localizedStreetName(way.Tags)
	if street.Value() == "" {
		street = unique.Make(name)
		name = ""
	}
	nameHandle := unique.Make(name)
	oneway := parseOneway(way.Tags)

	out := make([]geoPoint, 0, len(way.Nodes))
	var prevNodeID uint32
	havePrev := false

	for _, node := range way.Nodes {
		lon, lat, ok := f.resolveWayNode(node)
		if !ok {
			continue
		}

		nodeID, first := f.claimGraphNode(node.ID)
		if first {
			point := orb.Point{lon, lat}
			out = append(out, geoPoint{
				Point:       point,
				Name:        name,
				Street:      street,
				HouseNumber: unique.Make(""),
				City:        f.localizedCityAddr(way.Tags, point),
				Region:      f.localizedRegion(point),
				Weight:      weightRoad,
				Type:        cachemodel.GeoObjectRoad,
				GraphNode:   nodeID,
			})
		}

		// A way may repeat a node consecutively (real data does: e.g. OSM way
		// 261379237 repeats node 2669983324). The second occurrence would make
		// a self-loop edge, which is meaningless in the graph, so skip it.
		if havePrev && prevNodeID != nodeID {
			f.parsedItems <- parseItem{
				IsEdge: true,
				Edge: cachemodel.GraphEdge{
					FromNode: prevNodeID,
					ToNode:   nodeID,
					Street:   street,
					Name:     nameHandle,
					Class:    class,
					Oneway:   oneway,
				},
			}
		}
		prevNodeID = nodeID
		havePrev = true
	}

	return out
}

// resolveWayNode returns the coordinates of a way node, falling back to the
// node database when the way member does not carry them.
func (f *GeoGen) resolveWayNode(node osm.WayNode) (lon, lat float64, ok bool) {
	if node.Lat != 0 && node.Lon != 0 {
		return node.Lon, node.Lat, true
	}

	p, err := f.osmdb.GetNode(node.ID)
	if err != nil {
		f.log.Error("failed to get node", "id", node.ID, "error", err.Error())
		return 0, 0, false
	}
	if p.Lat == 0 && p.Lon == 0 {
		f.log.Error("node has no coordinates", "id", node.ID)
		return 0, 0, false
	}
	return p.Lon, p.Lat, true
}

// claimGraphNode returns the dense graph node id for an OSM node. The first
// caller to claim a node reports first=true and is responsible for emitting the
// node's cache point; later callers reuse the id.
func (f *GeoGen) claimGraphNode(node osm.NodeID) (id uint32, first bool) {
	proposed := f.graphNodeSeq.Add(1)
	actual, loaded := f.graphNodes.LoadOrStore(node, proposed)
	if loaded {
		f.graphNodesDupes.Add(1)
		return actual, false
	}
	return proposed, true
}

// parseOneway maps OSM oneway tagging to the graph direction enum. Roundabouts
// are one-way by convention unless tagged otherwise.
func parseOneway(tags osm.Tags) uint8 {
	switch tags.Find("oneway") {
	case "yes", "true", "1":
		return cachemodel.GraphOnewayForward
	case "-1", "reverse":
		return cachemodel.GraphOnewayBackward
	case "no", "false", "0", "reversible":
		return cachemodel.GraphOnewayBoth
	}

	if tags.Find("junction") == "roundabout" {
		return cachemodel.GraphOnewayForward
	}
	return cachemodel.GraphOnewayBoth
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
				Point:       p,
				Weight:      weightBuilding,
				Type:        cachemodel.GeoObjectBuilding,
				Name:        f.localizedName(rel.Tags),
				Street:      f.localizedStreetName(rel.Tags),
				HouseNumber: unique.Make(rel.Tags.Find("addr:housenumber")),
				City:        f.localizedCityAddr(rel.Tags, p),
				Region:      f.localizedRegion(p),
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

			Weight:      weight,
			Type:        cachemodel.GeoObjectArea,
			Name:        name,
			Street:      unique.Make(""),
			HouseNumber: unique.Make(""),
			City:        f.localizedCityAddr(rel.Tags, p),
			Region:      f.localizedRegion(p),
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

func fillPolygonWithPoints(poly orb.MultiPolygon, distance float64) []orb.Point {
	// 1. Get the bounding box of the polygon
	bound := poly.Bound()
	points := poissondisc.Sample(bound.Min.X(), bound.Min.Y(), bound.Max.X(), bound.Max.Y(), distance, 10, nil)

	// 2. Filter points inside the polygon
	pointsInside := make([]orb.Point, 0)
	for _, p := range points {
		point := orb.Point{p.X, p.Y}
		if planar.MultiPolygonContains(poly, point) {
			pointsInside = append(pointsInside, point)
		}
	}

	return pointsInside
}
