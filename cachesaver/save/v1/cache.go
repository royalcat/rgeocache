//go:generate protoc --go_out=. --go_opt=paths=source_relative cache.proto
package savev1

import (
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

const COMPATIBILITY_LEVEL = 2

type Point struct {
	Lat, Lon float64

	Name        string
	Street      uint32
	HouseNumber string
	City        uint32
	Region      uint32
}

type Cache struct {
	// Values deduplication
	Streets []string
	Cities  []string
	Regions []string

	Points []Point
}

func CacheFromPoints(input []kdbush.Point[geomodel.Info]) Cache {
	points := []Point{}
	streets := uniqueMap{}
	cities := uniqueMap{}
	regions := uniqueMap{}

	for _, p := range input {
		streetIndex := streets.Add(p.Data.Street)
		cityIndex := cities.Add(p.Data.City)
		regionIndex := regions.Add(p.Data.Region)

		points = append(points, Point{
			Lat:         p.X,
			Lon:         p.Y,
			Name:        p.Data.Name,
			Street:      uint32(streetIndex),
			HouseNumber: p.Data.HouseNumber,
			City:        uint32(cityIndex),
			Region:      uint32(regionIndex),
		})
	}

	return Cache{
		Streets: streets.Slice(),
		Cities:  cities.Slice(),
		Regions: regions.Slice(),

		Points: points,
	}
}
