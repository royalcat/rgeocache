package geoparser

import (
	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
)

// relCacheKind says which border index a cached relation belongs to.
type relCacheKind uint8

const (
	relCacheNone relCacheKind = iota
	relCachePlace
	relCacheRegion
)

// relCacheResult is what pre-caching one relation produces. Workers only
// compute it; fillRelCache applies the results in relation order, so the
// localization map and the border indexes never depend on scheduling.
type relCacheResult struct {
	kind relCacheKind
	name string
	poly orb.MultiPolygon

	// locOfficial -> locLocalized is a name translation to remember. Empty
	// locOfficial means there is none.
	locOfficial, locLocalized string
}

// localizationPair returns the official -> localized name translation carried
// by tags, if any.
func (f *GeoGen) localizationPair(tags osm.Tags) (official, localized string) {
	officialName := tags.Find(nameKey)
	localizedName := tags.Find(nameKey + ":" + f.config.PreferredLocalization)
	if officialName != "" && localizedName != "" && officialName != localizedName {
		return officialName, localizedName
	}
	return "", ""
}

func (f *GeoGen) cacheRel(rel *osm.Relation) relCacheResult {
	const regionAdminLevel = "4"

	switch rel.Tags.Find("type") {
	case "boundary", "multipolygon":
		if rel.Tags.Find("admin_level") == regionAdminLevel {
			return f.cacheRelBorder(rel, relCacheRegion)
		}
		switch rel.Tags.Find("place") {
		case "city", "town", "village", "hamlet", "isolated_dwelling", "farm":
			return f.cacheRelBorder(rel, relCachePlace)
		}
	case "associatedStreet", "route":
		official, localized := f.localizationPair(rel.Tags)
		return relCacheResult{locOfficial: official, locLocalized: localized}
	}
	return relCacheResult{}
}

// cacheRelBorder builds the border of a place or region relation. The
// localization pair is only recorded when the border itself is usable.
func (f *GeoGen) cacheRelBorder(rel *osm.Relation, kind relCacheKind) relCacheResult {
	name := rel.Tags.Find(nameKey)

	log := f.log.With("id", rel.ID).With("name", name)

	tags := rel.TagMap()
	if tags["type"] != "multipolygon" && tags["type"] != "boundary" {
		return relCacheResult{}
	}

	mpoly, err := f.buildPolygon(rel.Members)
	if err != nil {
		log.Error("Error building polygon", "error", err.Error())
		return relCacheResult{}
	}

	if mpoly.Bound().IsZero() || len(mpoly) == 0 {
		log.Warn("Zero bound place")
		return relCacheResult{}
	}

	if name == "" {
		return relCacheResult{}
	}

	official, localized := f.localizationPair(rel.Tags)
	return relCacheResult{
		kind:         kind,
		name:         name,
		poly:         mpoly,
		locOfficial:  official,
		locLocalized: localized,
	}
}

// applyRelCache records one relation's result. It is called in relation order
// from a single goroutine.
//
// When two relations translate the same official name differently, the first
// in relation order wins, and the conflict is counted.
func (f *GeoGen) applyRelCache(r relCacheResult) {
	if r.locOfficial != "" {
		if existing, loaded := f.localizationCache.LoadOrStore(r.locOfficial, r.locLocalized); loaded && existing != r.locLocalized {
			f.localizationConflicts++
		}
	}

	switch r.kind {
	case relCachePlace:
		f.placeIndex.InsertBorderKeyed(r.name, r.name, r.poly)
	case relCacheRegion:
		f.regionIndex.InsertBorderKeyed(r.name, r.name, r.poly)
	}
}
