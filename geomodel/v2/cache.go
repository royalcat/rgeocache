package geomodelv2

import (
	"slices"

	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

type Info struct {
	Name        string
	Street      uint64
	HouseNumber string
	City        uint64
	Region      uint64
}

type Cache struct {
	Points  []kdbush.Point[Info]
	Streets []string
	Cities  []string
	Regions []string
}

func GenerateV2CacheFromV1(points []kdbush.Point[geomodel.Info]) {
	c := Cache{
		Points:  []kdbush.Point[Info]{},
		Streets: []string{},
		Cities:  []string{},
		Regions: []string{},
	}

	for _, p := range points {
		streetIndex := slices.Index(c.Streets, p.Data.Street)
		if streetIndex == -1 {
			c.Streets = append(c.Streets, p.Data.Street)
			streetIndex = len(c.Streets) - 1
		}

		cityIndex := slices.Index(c.Cities, p.Data.City)
		if cityIndex == -1 {
			c.Cities = append(c.Cities, p.Data.City)
			cityIndex = len(c.Cities) - 1
		}

		regionIndex := slices.Index(c.Regions, p.Data.Region)
		if regionIndex == -1 {
			c.Regions = append(c.Regions, p.Data.Region)
			regionIndex = len(c.Regions) - 1
		}

		c.Points = append(c.Points, kdbush.Point[Info]{
			X: p.X,
			Y: p.Y,
			Data: Info{
				Name:        p.Data.Name,
				Street:      uint64(streetIndex),
				HouseNumber: p.Data.HouseNumber,
				City:        uint64(cityIndex),
				Region:      uint64(regionIndex),
			},
		})
	}
}
