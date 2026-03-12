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
}

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
