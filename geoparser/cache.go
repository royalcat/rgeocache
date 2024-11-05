package geoparser

import (
	"slices"

	"github.com/paulmach/osm"
	"github.com/sirupsen/logrus"
)

// func (f *GeoGen) cacheNode(node *osm.Node) {
// 	f.nodeCache.Set(node.ID, cachePoint{node.Lat, node.Lon})
// }

// func (f *GeoGen) cacheWay(way *osm.Way) {
// 	log := logrus.WithField("id", way.ID)
// 	ls := makeLineString(f.nodeCache, way.Nodes)

// 	if len(ls) == 0 {
// 		log.Warn("No line string for way")
// 		return
// 	}

// 	f.wayCache.Set(way.ID, cacheWay(ls))

// 	if highway := way.Tags.Find("highway"); highway != "" {
// 		f.cacheLocalization(way.Tags)
// 	}
// }

func (f *GeoGen) cacheLocalization(tags osm.Tags) {
	name := tags.Find(nameKey)
	localizedName := tags.Find(nameKey + ":" + f.preferredLocalization)
	if name != "" && localizedName != "" && name != localizedName {
		f.localizationCache.Store(name, localizedName)
	}
}

var cachablePlaces = []string{"city", "town", "village", "hamlet", "isolated_dwelling", "farm"}

func (f *GeoGen) cacheRel(rel *osm.Relation) {
	name := rel.Tags.Find(nameKey)

	_ = name

	if slices.Contains(cachablePlaces, rel.Tags.Find("place")) {
		f.cacheRelPlace(rel)
	}

	if rel.Tags.Find("type") == "associatedStreet" {
		f.cacheLocalization(rel.Tags)
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
		f.placeCache.Store(rel.ID, cachePlace{
			Name:         name,
			Bound:        mpoly.Bound(),
			MultiPolygon: mpoly,
		})
	}
}
