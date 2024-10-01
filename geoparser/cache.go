package geoparser

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/cheggaaa/pb/v3/termutil"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	"github.com/royalcat/btrgo"
	"github.com/sirupsen/logrus"
)

func (f *GeoGen) fillCache(ctx context.Context, base string) error {
	file, err := os.Open(base)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, _ := file.Stat()

	// The third parameter is the number of parallel decoders to use.
	scanner := osmpbf.New(ctx, file, runtime.GOMAXPROCS(-1)-1)
	defer scanner.Close()

	bar := pb.Start64(stat.Size())
	bar.Set("prefix", "1/2 filling cache")
	bar.Set(pb.Bytes, true)
	bar.SetRefreshRate(time.Second)
	if w, err := termutil.TerminalWidth(); w == 0 || err != nil {
		bar.SetTemplateString(`{{with string . "prefix"}}{{.}} {{end}}{{counters . }} {{bar . }} {{percent . }} {{speed . }} {{rtime . "ETA %s"}}{{with string . "suffix"}} {{.}}{{end}}` + "\n")
	}

	for scanner.Scan() {
		bar.SetCurrent(scanner.FullyScannedBytes())

		switch o := scanner.Object().(type) {
		case *osm.Node:
			f.cacheNode(o)

		case *osm.Way:
			f.cacheWay(o)

		case *osm.Relation:
			f.cacheRel(o)
		}
	}
	bar.Finish()

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
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
