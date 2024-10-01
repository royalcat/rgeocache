package geoparser

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"

	"github.com/alitto/pond"
	"github.com/cheggaaa/pb/v3"
	"github.com/cheggaaa/pb/v3/termutil"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	"github.com/sirupsen/logrus"
)

func (f *GeoGen) parse(ctx context.Context, base string) error {
	file, err := os.Open(base)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, _ := file.Stat()

	// The third parameter is the number of parallel decoders to use.
	scanner := osmpbf.New(ctx, file, f.threads)
	defer scanner.Close()

	bar := pb.Start64(stat.Size())
	bar.Set("prefix", "2/2 generating database")
	bar.Set(pb.Bytes, true)
	bar.SetRefreshRate(time.Second)
	if w, err := termutil.TerminalWidth(); w == 0 || err != nil {
		bar.SetTemplateString(`{{with string . "prefix"}}{{.}} {{end}}{{counters . }} {{bar . }} {{percent . }} {{speed . }} {{rtime . "ETA %s"}}{{with string . "suffix"}} {{.}}{{end}}` + "\n")
	}

	pool := pond.New(f.threads, f.threads*2, pond.Strategy(pond.Lazy()))

	for scanner.Scan() {
		bar.SetCurrent(scanner.FullyScannedBytes())

		switch o := scanner.Object().(type) {
		case *osm.Node:
			if point, ok := f.parseNode(o); ok {
				f.pointsMutex.Lock()
				f.points = append(f.points, point)
				f.pointsMutex.Unlock()
			}

		case *osm.Way:
			if point, ok := f.parseWay(o); ok {
				f.pointsMutex.Lock()
				f.points = append(f.points, point)
				f.pointsMutex.Unlock()
			}

		case *osm.Relation:
			if points := f.parseRelation(o); len(points) > 0 {
				f.pointsMutex.Lock()
				f.points = append(f.points, points...)
				f.pointsMutex.Unlock()
			}

		}
	}
	pool.StopAndWait()
	bar.Finish()

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
}

func (f *GeoGen) parseNode(node *osm.Node) (kdbush.Point[geomodel.Info], bool) {
	street := node.Tags.Find("addr:street")
	housenumber := node.Tags.Find("addr:housenumber")
	building := node.Tags.Find("building")

	if housenumber != "" && street != "" && building != "" {
		return kdbush.Point[geomodel.Info]{
			X: node.Lat,
			Y: node.Lon,
			Data: geomodel.Info{
				Name:        f.localizedName(node.Tags),
				Street:      f.localizedStreetName(node.Tags),
				HouseNumber: housenumber,
				City:        f.localizeCityAddr(node.Tags, orb.Point{node.Lat, node.Lon}),
			},
		}, true
	}

	return kdbush.Point[geomodel.Info]{}, false
}

func (f *GeoGen) parseWay(way *osm.Way) (kdbush.Point[geomodel.Info], bool) {
	street := way.Tags.Find("addr:street")
	housenumber := way.Tags.Find("addr:housenumber")
	building := way.Tags.Find("building")

	if housenumber != "" && street != "" && building != "" {
		lat, lon := f.calcWayCenter(way)

		return kdbush.Point[geomodel.Info]{
			X: lat,
			Y: lon,
			Data: geomodel.Info{
				Name:        f.localizedName(way.Tags),
				Street:      f.localizedStreetName(way.Tags),
				HouseNumber: housenumber,
				City:        f.localizeCityAddr(way.Tags, orb.Point{lat, lon}),
			},
		}, true
	}

	return kdbush.Point[geomodel.Info]{}, false
}

func (f *GeoGen) parseRelation(rel *osm.Relation) []kdbush.Point[geomodel.Info] {
	points := []kdbush.Point[geomodel.Info]{}

	tags := rel.TagMap()
	street := tags["addr:street"]
	housenumber := tags["addr:housenumber"]
	building := tags["building"]

	if housenumber != "" && street != "" && building != "" {
		return f.parseRelationBuilding(rel)
	}

	if tags["route"] == "road" && tags["type"] == "route" && strings.Contains(tags["network"], "national") {
		return f.parseRelationHighway(rel)
	}

	return points
}

func (f *GeoGen) parseRelationBuilding(rel *osm.Relation) []kdbush.Point[geomodel.Info] {
	points := []kdbush.Point[geomodel.Info]{}
	tags := rel.TagMap()

	if tags["type"] == "multipolygon" {
		mpoly, err := f.buildPolygon(rel.Members)
		if err != nil {
			logrus.Errorf("Error building polygon: %s", err.Error())
			return points
		}
		if mpoly == nil && len(mpoly) == 0 {
			logrus.Errorf("Empty polygon: %s", tags["name"])
			return points
		}

		for _, poly := range mpoly {
			p, _ := planar.CentroidArea(poly)

			points = append(points, kdbush.Point[geomodel.Info]{
				X: p[0],
				Y: p[1],
				Data: geomodel.Info{
					Name:        f.localizedName(rel.Tags),
					Street:      f.localizedStreetName(rel.Tags),
					HouseNumber: rel.Tags.Find("addr:housenumber"),
					City:        f.localizeCityAddr(rel.Tags, p),
				},
			})
		}
	}

	return points
}

func (f *GeoGen) parseRelationHighway(rel *osm.Relation) []kdbush.Point[geomodel.Info] {
	points := []kdbush.Point[geomodel.Info]{}

	for _, m := range rel.Members {
		if m.Type != osm.TypeWay {
			continue
		}

		if way, ok := f.wayCache.Get(m.Ref); ok {
			for _, point := range orb.LineString(way) {

				points = append(points, kdbush.Point[geomodel.Info]{
					X: point[0],
					Y: point[1],
					Data: geomodel.Info{
						Name:   f.localizedName(rel.Tags),
						Street: f.localizedStreetName(rel.Tags),
						City:   f.localizeCityAddr(rel.Tags, point),
					},
				})
			}
		}
	}
	return points
}
