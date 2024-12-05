package geoparser

import (
	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
)

const nameKey = "name"

func (f *GeoGen) localizedName(tags osm.Tags) string {
	name := tags.Find(nameKey)

	if f.preferredLocalization != "" {
		if localizedName := tags.Find(nameKey + ":" + f.preferredLocalization); localizedName != "" {
			return localizedName
		}

		if localizedName, ok := f.localizationCache.Load(name); ok {
			return localizedName
		}
	}

	return tags.Find(nameKey)
}

const cityAddrKey = "addr:city"

func (f *GeoGen) localizeCityAddr(tags osm.Tags, point orb.Point) string {
	name := tags.Find(cityAddrKey)

	if f.preferredLocalization == "" {
		if name != "" {
			return name
		}
		return f.calcPlace(point).Name
	}

	if localizedName := tags.Find(cityAddrKey + ":" + f.preferredLocalization); localizedName != "" {
		return localizedName
	}

	if localizedName, ok := f.localizationCache.Load(name); ok {
		return localizedName
	}

	if calcPlaceName := f.calcPlace(point).Name; calcPlaceName != "" {
		if localizedName, ok := f.localizationCache.Load(calcPlaceName); ok {
			return localizedName
		}

		return calcPlaceName
	}

	return name
}

const addrStreetKey = "addr:street"

func (f *GeoGen) localizedStreetName(tags osm.Tags) string {
	name := tags.Find(addrStreetKey)

	if f.preferredLocalization == "" {
		return name
	}

	if localizedName := tags.Find(addrStreetKey + ":" + f.preferredLocalization); localizedName != "" {
		return localizedName
	}

	if localizedName, ok := f.localizationCache.Load(name); ok {
		return localizedName
	}

	return name
}
