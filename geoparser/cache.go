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

func (f *GeoGen) fillCache(base string) error {
	file, err := os.Open(base)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, _ := file.Stat()

	// The third parameter is the number of parallel decoders to use.
	scanner := osmpbf.New(context.Background(), file, runtime.GOMAXPROCS(-1)-1)
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
}

var goodPlaces = []string{"city", "town", "village", "hamlet", "isolated_dwelling", "farm"}

func (f *GeoGen) cacheRel(rel *osm.Relation) {
	log := logrus.WithField("id", rel.ID).WithField("name", rel.Tags.Find("name"))

	tags := rel.TagMap()
	place := tags["place"]

	if btrgo.InSlice(goodPlaces, place) && (tags["type"] == "multipolygon" || tags["type"] == "boundary") {
		mpoly, err := f.buildPolygon(rel.Members)
		if err != nil {
			log.Errorf("Error building polygon: %s", err.Error())
			return
		}
		if mpoly.Bound().IsZero() || len(mpoly) == 0 {
			log.Warnf("Zero bound city: %s", tags["name"])
			return
		}

		f.cityCache.Set(int64(rel.ID), cacheCity{
			Name:         tags["name"],
			Bound:        mpoly.Bound(),
			MultiPolygon: mpoly,
		})
	}
}
