package savev1

import (
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
	Streets []string
	Cities  []string
	Regions []string

	Points []saveproto.Point
	Zones  []saveproto.Zone
}

func cacheFromPoints(inputPoints []cachemodel.Point, inputRegions []cachemodel.Zone, metadata cachemodel.Metadata) cache {
	points := []saveproto.Point{}
	streets := newUniqueMap()
	cities := newUniqueMap()
	regionsNames := newUniqueMap()
	for _, p := range inputPoints {
		streetIndex := streets.Add(p.Data.Street.Value())
		cityIndex := cities.Add(p.Data.City.Value())
		regionIndex := regionsNames.Add(p.Data.Region.Value())

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

	regions := []saveproto.Zone{}
	for _, z := range inputRegions {
		nameIndex := regionsNames.Add(z.Name.Value())
		regions = append(regions, saveproto.Zone{
			Name:         uint32(nameIndex),
			Bounds:       mapBoundsFromOrb(z.Bounds),
			MultiPolygon: mapMultiPolygonFromOrb(z.Polygon),
		})
	}

	return cache{
		Version:     metadata.Version,
		Locale:      metadata.Locale,
		DateCreated: metadata.DateCreated.Format(time.RFC3339),

		Streets: streets.Slice(),
		Cities:  cities.Slice(),
		Regions: regionsNames.Slice(),

		Points: points,
		Zones:  regions,
	}
}
