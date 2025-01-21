package geomodelv2

import (
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

func GenerateV2CacheFromV1(input []kdbush.Point[geomodel.Info]) Cache {
	points := []kdbush.Point[Info]{}
	streets := uniqueMap{}
	cities := uniqueMap{}
	regions := uniqueMap{}

	for _, p := range input {
		streetIndex := streets.Add(p.Data.Street)
		cityIndex := cities.Add(p.Data.City)
		regionIndex := regions.Add(p.Data.Region)

		points = append(points, kdbush.Point[Info]{
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

	return Cache{
		Points:  points,
		Streets: streets.Slice(),
		Cities:  cities.Slice(),
		Regions: regions.Slice(),
	}
}

type uniqueMap struct {
	m map[string]int
	i int
}

func (uq *uniqueMap) Add(val string) int {
	i, ok := uq.m[val]
	if !ok {
		uq.m[val] = uq.i
		uq.i++
		return uq.i - 1
	}
	return i
}

func (uq *uniqueMap) Get(val string) int {
	if i, ok := uq.m[val]; ok {
		return i
	}
	return -1
}

func (uq *uniqueMap) Slice() []string {
	s := make([]string, uq.i)
	for k, v := range uq.m {
		s[v] = k
	}
	return s
}
