package geoparser

import (
	"unique"

	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
)

// emptyHandle is the interned empty string. Road and area points carry empty
// house numbers and streets; interning them once avoids a hash lookup per
// generated point.
var emptyHandle = unique.Make("")

func (f *GeoGen) getHighwayName(tags osm.Tags) string {
	ref := tags.Find("ref")
	highwayName := f.localizedName(tags)
	if ref == "" {
		return highwayName
	}
	return ref + " " + highwayName
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

	return name
}

const cityAddrKey = "addr:city"

// cityAddrFromTags resolves the city address that depends only on an object's
// tags. A way or relation can compute it once instead of once per point; ok is
// false when the city has to be looked up from each point's location.
func (f *GeoGen) cityAddrFromTags(tags osm.Tags) (unique.Handle[string], bool) {
	name := tags.Find(cityAddrKey)

	if f.config.PreferredLocalization == "" {
		if name != "" {
			return unique.Make(name), true
		}
		return emptyHandle, false
	}

	if localizedName := tags.Find(cityAddrKey + ":" + f.config.PreferredLocalization); localizedName != "" {
		return unique.Make(localizedName), true
	}

	if localizedName, ok := f.localizationCache.Load(name); ok {
		return unique.Make(localizedName), true
	}

	return emptyHandle, false
}

func (f *GeoGen) localizedCityAddr(tags osm.Tags, point orb.Point) unique.Handle[string] {
	name := tags.Find(cityAddrKey)

	if f.config.PreferredLocalization == "" {
		if name != "" {
			return unique.Make(name)
		}
		return unique.Make(f.calcPlace(point))
	}

	if localizedName := tags.Find(cityAddrKey + ":" + f.config.PreferredLocalization); localizedName != "" {
		return unique.Make(localizedName)
	}

	if localizedName, ok := f.localizationCache.Load(name); ok {
		return unique.Make(localizedName)
	}

	if calcPlaceName := f.calcPlace(point); calcPlaceName != "" {
		if localizedName, ok := f.localizationCache.Load(calcPlaceName); ok {
			return unique.Make(localizedName)
		}

		return unique.Make(calcPlaceName)
	}

	return unique.Make(name)
}

// cityAt returns the city handle for a point in an object that already
// precomputed its tag-only city address.
func (f *GeoGen) cityAt(handle unique.Handle[string], fromTags bool, tags osm.Tags, point orb.Point) unique.Handle[string] {
	if fromTags {
		return handle
	}
	return f.localizedCityAddr(tags, point)
}

const addrStreetKey = "addr:street"

func (f *GeoGen) localizedStreetName(tags osm.Tags) unique.Handle[string] {
	name := tags.Find(addrStreetKey)

	if f.config.PreferredLocalization == "" {
		return unique.Make(name)
	}

	if localizedName := tags.Find(addrStreetKey + ":" + f.config.PreferredLocalization); localizedName != "" {
		return unique.Make(localizedName)
	}

	if localizedName, ok := f.localizationCache.Load(name); ok {
		return unique.Make(localizedName)
	}

	return unique.Make(name)
}

func (f *GeoGen) localizedRegion(point orb.Point) unique.Handle[string] {

	if regionName := f.calcRegion(point); regionName != "" {
		if localizedName, ok := f.localizationCache.Load(regionName); ok {
			return unique.Make(localizedName)
		}

		return unique.Make(regionName)
	}

	return emptyHandle
}
