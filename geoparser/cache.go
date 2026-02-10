package geoparser

import (
	"github.com/paulmach/osm"
	"github.com/royalcat/rgeocache/cachesaver"
)

func (f *GeoGen) cacheLocalization(tags osm.Tags) {
	officialName := tags.Find(nameKey)
	localizedName := tags.Find(nameKey + ":" + f.config.PreferredLocalization)
	if officialName != "" && localizedName != "" && officialName != localizedName {
		f.localizationCache.Store(officialName, localizedName)
	}
}

func (f *GeoGen) cacheRel(rel *osm.Relation) {
	const regionAdminLevel = "4"

	switch rel.Tags.Find("type") {
	case "boundary", "multipolygon":
		if rel.Tags.Find("admin_level") == regionAdminLevel {
			f.cacheRelRegion(rel)
			return
		}
		switch rel.Tags.Find("place") {
		case "city", "town", "village", "hamlet", "isolated_dwelling", "farm":
			f.cacheRelPlace(rel)
			return
		}
	case "associatedStreet", "route":
		f.cacheLocalization(rel.Tags)
	}
}

func (f *GeoGen) cacheRelPlace(rel *osm.Relation) {
	name := rel.Tags.Find(nameKey)

	log := f.log.With("id", rel.ID).With("name", name)

	tags := rel.TagMap()
	if tags["type"] == "multipolygon" || tags["type"] == "boundary" {

		mpoly, err := f.buildPolygon(rel.Members)
		if err != nil {
			log.Error("Error building polygon", "error", err.Error())
			return
		}

		if mpoly.Bound().IsZero() || len(mpoly) == 0 {
			log.Warn("Zero bound place")
			return
		}

		if name == "" {
			return
		}

		f.cacheLocalization(rel.Tags)

		f.placeIndex.InsertBorder(name, mpoly)
	}
}

func (f *GeoGen) cacheRelRegion(rel *osm.Relation) {
	name := rel.Tags.Find(nameKey)

	log := f.log.With("id", rel.ID).With("name", name)

	tags := rel.TagMap()
	if tags["type"] == "multipolygon" || tags["type"] == "boundary" {

		mpoly, err := f.buildPolygon(rel.Members)
		if err != nil {
			log.Error("Error building polygon", "error", err.Error())
			return
		}

		if mpoly.Bound().IsZero() || len(mpoly) == 0 {
			log.Warn("Zero bound place")
			return
		}

		if name == "" {
			return
		}

		f.cacheLocalization(rel.Tags)
		f.regionIndex.InsertBorder(name, mpoly)

		rawMPoly := make([][][2]float64, 0, len(mpoly))
		for _, poly := range mpoly {
			rawPoly := make([][2]float64, 0, len(poly))
			for _, ring := range poly {
				for _, point := range ring {
					rawPoly = append(rawPoly, [2]float64{point.X(), point.Y()})
				}
			}
			rawMPoly = append(rawMPoly, rawPoly)
		}
		bound := mpoly.Bound()
		f.zones = append(f.zones, cachesaver.Zone{
			Name:    name,
			Bounds:  [4]float64{bound.Min.X(), bound.Min.Y(), bound.Max.X(), bound.Max.Y()},
			Polygon: rawMPoly,
		})
	}
}
