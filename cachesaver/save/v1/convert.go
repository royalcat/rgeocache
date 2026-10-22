package savev1

import (
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
)

// ZoneProto converts a cache zone to its v1 representation. nameIndex is the
// index of the zone name in StringsCache.Regions.
func ZoneProto(z cachemodel.Zone, nameIndex uint32) *saveproto.Zone {
	return &saveproto.Zone{
		Name:         nameIndex,
		Bounds:       mapBoundsFromOrb(z.Bounds),
		MultiPolygon: mapMultiPolygonFromOrb(z.Polygon),
	}
}

// ZoneTypeProto maps a cache zone type to its v1 blob type.
func ZoneTypeProto(t cachemodel.ZoneType) saveproto.ZoneType {
	return mapZoneType(t)
}
