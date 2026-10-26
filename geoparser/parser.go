package geoparser

import (
	"log/slog"
	"math/rand"
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

func (f *GeoGen) parseObject(o osm.Object, claimSeq uint32) {
	switch obj := o.(type) {
	case *osm.Node:
		f.seenNodes.Add(1)
		if point, ok := f.parseNode(obj); ok {
			f.send(parseItem{Points: []geoPoint{point}})
		}
	case *osm.Way:
		f.seenWays.Add(1)
		f.send(f.parseWay(obj, claimSeq))
	case *osm.Relation:
		f.seenRelations.Add(1)
		for _, item := range f.parseRelation(obj) {
			f.send(item)
		}
	}
}

// send hands one batch to the collector. Empty batches are dropped.
func (f *GeoGen) send(item parseItem) {
	if len(item.Points) == 0 && len(item.Edges) == 0 {
		return
	}
	f.parsedItems <- item
}

// parseItem is one batch of parse output: the points of one OSM object (or
// relation member way) and the graph edges between them. Edge endpoints are
// indexes into Points; the collector translates them to point occurrences.
type parseItem struct {
	Points []geoPoint
	Edges  []edgeRef
}

// edgeRef is a graph edge whose endpoints are local point indexes.
type edgeRef struct {
	FromLocal, ToLocal uint32
	Street, Name       unique.Handle[string]
	Class, Oneway      uint8
}

type geoPoint struct {
	orb.Point

	Name        string                `json:"name"`
	Street      unique.Handle[string] `json:"street"`
	HouseNumber unique.Handle[string] `json:"house_number"`
	City        unique.Handle[string] `json:"city"`
	Region      unique.Handle[string] `json:"region"`

	Weight uint8                    `json:"weight"`
	Type   cachemodel.GeoObjectType `json:"type"`

	// NodeKey identifies the OSM node shared by road shape points. It is a
	// unique synthetic key for interpolated points and 0 for non-road points.
	NodeKey uint64 `json:"node_key"`
	// ClaimSeq is the way's deterministic input order, used to pick which
	// way's attributes win at a shared node.
	ClaimSeq uint32 `json:"claim_seq"`
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

func (f *GeoGen) parseWay(way *osm.Way, claimSeq uint32) parseItem {
	if !f.parsedWays.Add(int64(way.ID)) {
		f.parsedWaysDupes.Add(1)
		return parseItem{}
	}

	if isBuilding(way.Tags) {
		return parseItem{Points: f.parseWayBuilding(way)}
	}
	if _, ok := highwayClasses[way.Tags.Find("highway")]; ok {
		return f.parseWayHighway(way, claimSeq)
	}

	return parseItem{}
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
	"motorway":     cachemodel.GraphClassMotorway,
	"trunk":        cachemodel.GraphClassTrunk,
	"primary":      cachemodel.GraphClassPrimary,
	"secondary":    cachemodel.GraphClassSecondary,
	"tertiary":     cachemodel.GraphClassTertiary,
	"residential":  cachemodel.GraphClassResidential,
	"unclassified": cachemodel.GraphClassUnclassified,
}

// parseWayHighway emits one point per way node occurrence and links consecutive
// occurrences into graph edges.
//
// Every occurrence is emitted, including nodes already seen on another way:
// the collector canonicalizes them later by OSM node id, dropping duplicates
// from the point table while keeping the shared junction. This keeps the
// generator's memory bounded — there is no OSM node id -> dense id map.
//
// A span without an intermediate shape node carries no road point for its whole
// length, which can exceed the reverse-geocoding search radius. Interpolated
// points get unique synthetic node keys, so they are never merged with anything
// else; the graph simplifier drops them again wherever the span is short
// enough.
func (f *GeoGen) parseWayHighway(way *osm.Way, claimSeq uint32) parseItem {
	class, ok := highwayClasses[way.Tags.Find("highway")]
	if !ok {
		return parseItem{}
	}

	name := f.getHighwayName(way.Tags)
	street := f.localizedStreetName(way.Tags)
	if street.Value() == "" {
		street = unique.Make(name)
		name = ""
	}
	nameHandle := unique.Make(name)
	oneway := parseOneway(way.Tags)

	out := parseItem{
		Points: make([]geoPoint, 0, len(way.Nodes)),
		Edges:  make([]edgeRef, 0, len(way.Nodes)),
	}

	var (
		prevLocal  int
		prevNodeID osm.NodeID
		prevLon    float64
		prevLat    float64
		havePrev   bool
	)

	for _, node := range way.Nodes {
		lon, lat, ok := f.resolveWayNode(node)
		if !ok {
			continue
		}

		local := len(out.Points)
		point := orb.Point{lon, lat}
		out.Points = append(out.Points, geoPoint{
			Point:       point,
			Name:        name,
			Street:      street,
			HouseNumber: unique.Make(""),
			City:        f.localizedCityAddr(way.Tags, point),
			Region:      f.localizedRegion(point),
			Weight:      weightRoad,
			Type:        cachemodel.GeoObjectRoad,
			NodeKey:     uint64(node.ID),
			ClaimSeq:    claimSeq,
		})

		// A way may repeat a node consecutively (real data does: e.g. OSM way
		// 261379237 repeats node 2669983324). The second occurrence would make
		// a self-loop edge, which is meaningless in the graph, so skip it. The
		// duplicate point is still emitted and canonicalized away later.
		if havePrev && prevNodeID != node.ID {
			fromLocal := prevLocal

			for _, p := range interpolateRoadSegment(prevLon, prevLat, lon, lat) {
				synthLocal := len(out.Points)
				out.Points = append(out.Points, geoPoint{
					Point:       p,
					Name:        name,
					Street:      street,
					HouseNumber: unique.Make(""),
					City:        f.localizedCityAddr(way.Tags, p),
					Region:      f.localizedRegion(p),
					Weight:      weightRoad,
					Type:        cachemodel.GeoObjectRoad,
					NodeKey:     f.syntheticNodeKey(),
					ClaimSeq:    claimSeq,
				})
				out.Edges = append(out.Edges, edgeRef{
					FromLocal: uint32(fromLocal),
					ToLocal:   uint32(synthLocal),
					Street:    street,
					Name:      nameHandle,
					Class:     class,
					Oneway:    oneway,
				})
				fromLocal = synthLocal
			}

			out.Edges = append(out.Edges, edgeRef{
				FromLocal: uint32(fromLocal),
				ToLocal:   uint32(local),
				Street:    street,
				Name:      nameHandle,
				Class:     class,
				Oneway:    oneway,
			})
		}

		prevLocal = local
		prevNodeID = node.ID
		prevLon, prevLat = lon, lat
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

func (f *GeoGen) parseRelation(rel *osm.Relation) []parseItem {
	if !f.parsedRelations.Add(int64(rel.ID)) {
		f.parsedRelationsDupes.Add(1)
		return nil
	}

	switch rel.Tags.Find("type") {
	case "multipolygon", "boundary":
		switch rel.Tags.Find("landuse") {
		case "quarry", "industrial":
			return pointsOnly(f.parseRelationArea(rel, weightAreaIndustrial))
		}
		if rel.Tags.Find("boundary") == "protected_area" {
			return pointsOnly(f.parseRelationArea(rel, weightAreaProtected))
		}
		if isBuilding(rel.Tags) {
			return pointsOnly(f.parseRelationBuilding(rel))
		}
		if rel.Tags.Find("boundary") == "administrative" {
			switch rel.Tags.Find("admin_level") {
			case "4":
				f.parseRelationRegion(rel)
				return nil
			case "2":
				f.parseRelationCountry(rel)
				return nil
			}
		}
	case "building":
		if rel.Tags.Find("route") == "road" && strings.Contains(rel.Tags.Find("network"), "national") {
			return f.parseRelationHighway(rel)
		}
	}

	return nil
}

func pointsOnly(points []geoPoint) []parseItem {
	if len(points) == 0 {
		return nil
	}
	return []parseItem{{Points: points}}
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

func (f *GeoGen) parseRelationHighway(rel *osm.Relation) []parseItem {
	var out []parseItem
	for _, m := range rel.Members {
		if m.Type != osm.TypeWay {
			continue
		}

		way, err := f.osmdb.GetWay(osm.WayID(m.Ref))
		if err != nil {
			f.log.Error("Error getting way", "id", m.Ref, "error", err.Error())
			continue
		}

		// Route-member ways are normally already parsed by the main way pass,
		// so this is a no-op. If one is missing from that pass there is no
		// stable input-order claim for it; 0 keeps the canonicalization
		// deterministic (and the way is still emitted).
		item := f.parseWay(way, 0)
		if len(item.Points) > 0 || len(item.Edges) > 0 {
			out = append(out, item)
		}
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

	points := fillPolygonWithPoints(poly, 0.01/2, int64(rel.ID))

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

func fillPolygonWithPoints(poly orb.MultiPolygon, distance float64, seed int64) []orb.Point {
	// 1. Get the bounding box of the polygon
	bound := poly.Bound()
	points := poissondisc.Sample(
		bound.Min.X(),
		bound.Min.Y(),
		bound.Max.X(),
		bound.Max.Y(),
		distance,
		10,
		rand.New(rand.NewSource(seed)),
	)

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
