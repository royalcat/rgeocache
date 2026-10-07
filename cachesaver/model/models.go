package cachemodel

import (
	"time"
	"unique"

	"github.com/paulmach/orb"
	"github.com/royalcat/rgeocache/kdbush"
)

type Metadata struct {
	Version     uint32
	Locale      string
	DateCreated time.Time
}

type Point = kdbush.Point[Info]

type Info struct {
	Name        unique.Handle[string]
	Street      unique.Handle[string]
	HouseNumber unique.Handle[string]
	City        unique.Handle[string]
	Region      unique.Handle[string]
	Weight      uint8
	Type        GeoObjectType
}

// GeoObjectType is the explicit kind of a cached point. GeoObjectUnknown is the
// zero value, meaning the type was not recorded (legacy cache) and consumers
// should fall back to deriving the kind from Info.Weight.
type GeoObjectType uint8

const (
	GeoObjectUnknown  GeoObjectType = 0
	GeoObjectBuilding GeoObjectType = 1
	GeoObjectRoad     GeoObjectType = 2
	// GeoObjectArea covers industrial and protected areas alike.
	GeoObjectArea GeoObjectType = 3
)

// ItemKind discriminates the elements of a cache save stream.
type ItemKind uint8

const (
	ItemPoint ItemKind = 0
	ItemEdge  ItemKind = 1
)

// Item is one element of the streaming cache save input: either a point with
// its optional graph node id, or a graph edge.
type Item struct {
	Kind ItemKind
	// Point is valid when Kind == ItemPoint.
	Point Point
	// GraphNode is the dense graph node id of Point, 0 when the point is not a
	// graph node. Graph node ids are generator-internal: they are translated to
	// spatial index positions when the graph section is written.
	GraphNode uint32
	// Edge is valid when Kind == ItemEdge.
	Edge GraphEdge
}

// GraphEdge is a link between two graph nodes, carrying direction metadata.
//
// FromNode/ToNode are graph node ids in the direction of the source OSM way;
// for Oneway == GraphOnewayBackward travel is valid from ToNode to FromNode.
type GraphEdge struct {
	FromNode uint32
	ToNode   uint32
	Street   unique.Handle[string]
	Name     unique.Handle[string]
	Class    uint8
	Oneway   uint8
}

// Oneway values stored in the graph section.
const (
	GraphOnewayBoth     uint8 = 0
	GraphOnewayForward  uint8 = 1
	GraphOnewayBackward uint8 = 2
)

// Highway classes stored in the graph section.
const (
	GraphClassMotorway  uint8 = 1
	GraphClassTrunk     uint8 = 2
	GraphClassPrimary   uint8 = 3
	GraphClassSecondary uint8 = 4
	GraphClassTertiary  uint8 = 5
)

type ZoneType uint8

const (
	ZoneRegion ZoneType = iota + 1
	ZoneCountry
)

type Zone struct {
	Type    ZoneType
	Name    unique.Handle[string]
	Bounds  orb.Bound
	Polygon orb.MultiPolygon
}
