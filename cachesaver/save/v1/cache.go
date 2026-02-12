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
	Streets    []string
	Cities     []string
	Regions    []string
	ZonesNames []string

	Points []saveproto.Point
	Zones  []saveproto.Zone
}

func cacheFromPoints(inputPoints []cachemodel.Point, inputZones []cachemodel.Zone, metadata cachemodel.Metadata) cache {
	points := []saveproto.Point{}
	streets := newUniqueMap(1)
	cities := newUniqueMap(1)
	regions := newUniqueMap(1)
	for _, p := range inputPoints {
		streetIndex := streets.Add(p.Data.Street.Value())
		cityIndex := cities.Add(p.Data.City.Value())
		regionIndex := regions.Add(p.Data.Region.Value())

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

	zones := []saveproto.Zone{}
	zoneNames := newUniqueMap(1)
	for _, z := range inputZones {
		zones = append(zones, saveproto.Zone{
			Name: uint32(zoneNames.Add(z.Name.Value())),
			Bounds: &saveproto.Bounds{
				Max: &saveproto.LatLon{
					Lat: float32(z.Bounds.Max.Lat()),
					Lon: float32(z.Bounds.Max.Lon()),
				},
				Min: &saveproto.LatLon{
					Lat: float32(z.Bounds.Min.Lat()),
					Lon: float32(z.Bounds.Min.Lon()),
				},
			},
			MultiPolygon: mapToMultiPolygon(z.Polygon),
		})
	}

	return cache{
		Version:     metadata.Version,
		Locale:      metadata.Locale,
		DateCreated: metadata.DateCreated.Format(time.RFC3339),

		Streets:    streets.Slice(),
		Cities:     cities.Slice(),
		Regions:    regions.Slice(),
		ZonesNames: zoneNames.Slice(),

		Points: points,
		Zones:  zones,
	}
}
