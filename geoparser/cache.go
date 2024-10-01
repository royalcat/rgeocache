package geoparser

import (
	"github.com/paulmach/osm"
	"github.com/royalcat/btrgo"
	"github.com/sirupsen/logrus"
)

func (f *GeoGen) cacheObject(o osm.Object) {
	switch obj := o.(type) {
	case *osm.Node:
		f.cacheNode(obj)
	case *osm.Way:
		f.cacheWay(obj)
	case *osm.Relation:
		f.cacheRel(obj)
	}
}

func (f *GeoGen) cacheNode(node *osm.Node) {
	f.nodeCache.Set(int64(node.ID), cachePoint{node.Lat, node.Lon})
}

func (f *GeoGen) cacheWay(way *osm.Way) {
	ls := cacheWay(f.makeLineString(way.Nodes))
	f.wayCache.Set(int64(way.ID), ls)

	if highway := way.Tags.Find("highway"); highway != "" {
		f.cacheHighway(way)
	}
}

func (f *GeoGen) cacheHighway(way *osm.Way) {
	tags := way.TagMap()
	f.highwayCache.Set(int64(way.ID), cacheHighway{
		Name:          tags[nameKey],
		LocalizedName: f.localizedName(way.Tags),
	})
}

var cachablePlaces = []string{"city", "town", "village", "hamlet", "isolated_dwelling", "farm"}

func (f *GeoGen) cacheRel(rel *osm.Relation) {
	name := rel.Tags.Find(nameKey)

	_ = name

	if btrgo.InSlice(cachablePlaces, rel.Tags.Find("place")) {
		f.cacheRelPlace(rel)
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

		f.placeCache.Set(int64(rel.ID), cachePlace{
			Name:          name,
			LocalizedName: f.localizedName(rel.Tags),
			Bound:         mpoly.Bound(),
			MultiPolygon:  mpoly,
		})
	}
}
