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
