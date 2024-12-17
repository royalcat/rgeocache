package geoparser

import (
	"slices"

	"github.com/paulmach/osm"
	"github.com/sirupsen/logrus"
)

func (f *GeoGen) cacheLocalization(tags osm.Tags) {
	name := tags.Find(nameKey)
	localizedName := tags.Find(nameKey + ":" + f.preferredLocalization)
	if name != "" && localizedName != "" && name != localizedName {
		f.localizationCache.Store(name, localizedName)
	}
}

var cachablePlaces = []string{"city", "town", "village", "hamlet", "isolated_dwelling", "farm"}

const regionAdminLevel = "4"

func (f *GeoGen) cacheRel(rel *osm.Relation) {
	if slices.Contains(cachablePlaces, rel.Tags.Find("place")) {
		f.cacheRelPlace(rel)
	}

	if rel.Tags.Find("type") == "associatedStreet" {
		f.cacheLocalization(rel.Tags)
	}

	if rel.Tags.Find("admin_level") == regionAdminLevel {
		f.cacheRelRegion(rel)
	}
}

func (f *GeoGen) cacheRelPlace(rel *osm.Relation) {
	name := rel.Tags.Find(nameKey)

	log := logrus.WithField("id", rel.ID).WithField("name", name)

	tags := rel.TagMap()
	if tags["type"] == "multipolygon" || tags["type"] == "boundary" {

		mpoly, err := f.buildPolygon(rel.Members)
		if err != nil {
			log.Errorf("Error building polygon for %s: %s", name, err.Error())
			return
		}

		if mpoly.Bound().IsZero() || len(mpoly) == 0 {
			log.Warnf("Zero bound place: %s", name)
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

	log := logrus.WithField("id", rel.ID).WithField("name", name)

	tags := rel.TagMap()
	if tags["type"] == "multipolygon" || tags["type"] == "boundary" {

		mpoly, err := f.buildPolygon(rel.Members)
		if err != nil {
			log.Errorf("Error building polygon for %s: %s", name, err.Error())
			return
		}

		if mpoly.Bound().IsZero() || len(mpoly) == 0 {
			log.Warnf("Zero bound place: %s", name)
			return
		}

		if name == "" {
			return
		}

		f.cacheLocalization(rel.Tags)
		f.regoinIndex.InsertBorder(name, mpoly)
	}
}
