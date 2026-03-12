package savev1

import (
	"iter"
	"time"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
)

const COMPATIBILITY_LEVEL uint32 = 1

type cache struct {
	// Metadata
	Version     uint32
	DateCreated string
	Locale      string

	// Values deduplication
	StreetsNames []string
	CitiesNames  []string
	ZonesNames   []string

	Points    []saveproto.Point
	Regions   []*saveproto.Zone
	Countries []*saveproto.Zone
}

func cacheFromPoints(inputPoints iter.Seq[cachemodel.Point], inputZones iter.Seq[cachemodel.Zone], metadata cachemodel.Metadata) cache {
	points := []saveproto.Point{}
	streetsNames := newUniqueMap()
	citiesNames := newUniqueMap()
	zonesNames := newUniqueMap()
	for p := range inputPoints {
		streetIndex := streetsNames.Add(p.Data.Street.Value())
		cityIndex := citiesNames.Add(p.Data.City.Value())
		regionIndex := zonesNames.Add(p.Data.Region.Value())

		points = append(points, saveproto.Point{
			Latitude:    p.X,
			Longitude:   p.Y,
			Name:        p.Data.Name.Value(),
			Street:      uint32(streetIndex),
			HouseNumber: p.Data.HouseNumber.Value(),
			City:        uint32(cityIndex),
			Region:      uint32(regionIndex),
			Weight:      uint32(p.Data.Weight),
		})
	}

	regions := []*saveproto.Zone{}
	countries := []*saveproto.Zone{}
	for z := range inputZones {
		nameIndex := zonesNames.Add(z.Name.Value())
		protoZone := &saveproto.Zone{
			Name:         uint32(nameIndex),
			Bounds:       mapBoundsFromOrb(z.Bounds),
			MultiPolygon: mapMultiPolygonFromOrb(z.Polygon),
		}

		switch z.Type {
		case cachemodel.ZoneRegion:
			regions = append(regions, protoZone)
		case cachemodel.ZoneCountry:
			countries = append(countries, protoZone)
		}

	}

	return cache{
		Version:     metadata.Version,
		Locale:      metadata.Locale,
		DateCreated: metadata.DateCreated.Format(time.RFC3339),

		StreetsNames: streetsNames.Slice(),
		CitiesNames:  citiesNames.Slice(),
		ZonesNames:   zonesNames.Slice(),

		Points:  points,
		Regions: regions,
	}
}
