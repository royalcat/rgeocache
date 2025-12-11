package geoparser

import (
	"strings"

	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
)

func (f *GeoGen) getHighwayName(tags osm.Tags) string {
	ref := tags.Find("ref")
	highwayName := f.localizedName(tags)

	builder := strings.Builder{}
	if ref != "" {
		builder.WriteString(ref)
		builder.WriteString(" ")
	}
	if highwayName != "" {
		builder.WriteString(highwayName)
	}

	return builder.String()
}

const nameKey = "name"

func (f *GeoGen) localizedName(tags osm.Tags) string {
	name := tags.Find(nameKey)

	if f.config.PreferredLocalization != "" {
		if localizedName := tags.Find(nameKey + ":" + f.config.PreferredLocalization); localizedName != "" {
			return localizedName
		}

		if localizedName, ok := f.localizationCache.Load(name); ok {
			return localizedName
		}
	}

	return tags.Find(nameKey)
}

const cityAddrKey = "addr:city"

func (f *GeoGen) localizedCityAddr(tags osm.Tags, point orb.Point) string {
	name := tags.Find(cityAddrKey)

	if f.config.PreferredLocalization == "" {
		if name != "" {
			return name
		}
		return f.calcPlace(point)
	}

	if localizedName := tags.Find(cityAddrKey + ":" + f.config.PreferredLocalization); localizedName != "" {
		return localizedName
	}

	if localizedName, ok := f.localizationCache.Load(name); ok {
		return localizedName
	}

	if calcPlaceName := f.calcPlace(point); calcPlaceName != "" {
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

	if f.config.PreferredLocalization == "" {
		return name
	}

	if localizedName := tags.Find(addrStreetKey + ":" + f.config.PreferredLocalization); localizedName != "" {
		return localizedName
	}

	if localizedName, ok := f.localizationCache.Load(name); ok {
		return localizedName
	}

	return name
}

func (f *GeoGen) localizedRegion(point orb.Point) string {

	if regionName := f.calcRegion(point); regionName != "" {
		if localizedName, ok := f.localizationCache.Load(regionName); ok {
			return localizedName
		}

		return regionName
	}

	return ""
}
