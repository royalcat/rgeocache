package geoparser

import (
	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
)

const nameKey = "name"

func (f *GeoGen) localizedName(tags osm.Tags) string {
	if f.preferredLocalization != "" {
		if localizedName := tags.Find(nameKey + ":" + f.preferredLocalization); localizedName != "" {
			return localizedName
		}
	}

	return tags.Find(nameKey)
}

const cityAddrKey = "addr:city"

func (f *GeoGen) localizeCityAddr(tags osm.Tags, point orb.Point) string {
	cityAddr := tags.Find(cityAddrKey)

	if f.preferredLocalization == "" {
		name := tags.Find(cityAddrKey)
		if name != "" {
			return name
		}
		return f.calcPlace(point).Name
	}

	if localizedName := tags.Find(cityAddrKey + ":" + f.preferredLocalization); localizedName != "" {
		return localizedName
	}

	found := false
	f.placeCache.Range(func(key int64, value cachePlace) bool {
		if value.Name == cityAddr {
			found = true
			cityAddr = value.LocalizedName
			return false
		}
		return true
	})
	if found {
		return cityAddr
	}

	if calcCityName := f.calcPlace(point).BestName(); calcCityName != "" {
		return calcCityName
	}

	return cityAddr
}

const streetKey = "addr:street"

func (f *GeoGen) localizedStreetName(tags osm.Tags) string {
	if f.preferredLocalization == "" {
		return tags.Find(streetKey)
	}

	if localizedName := tags.Find(streetKey + ":" + f.preferredLocalization); localizedName != "" {
		return localizedName
	}

	name := tags.Find(streetKey)
	found := false
	f.highwayCache.Range(func(key int64, value cacheHighway) bool {
		if value.Name == name {
			found = true
			name = value.LocalizedName
			return false
		}
		return true
	})
	if found {
		return name
	}

	return name
}
