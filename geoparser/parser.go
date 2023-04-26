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

func (f *GeoGen) ParseOSMFile(base string) error {
	err := f.fillCache(base)
	if err != nil {
		return err
	}

	file, err := os.Open(base)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, _ := file.Stat()

	// The third parameter is the number of parallel decoders to use.
	scanner := osmpbf.New(context.Background(), file, f.threads)
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
			node := o
			pool.Submit(func() {
				if point, ok := f.parseNode(node); ok {
					f.pointsMutex.Lock()
					f.points = append(f.points, point)
					f.pointsMutex.Unlock()
				}
			})

		case *osm.Way:
			way := o
			pool.Submit(func() {
				if point, ok := f.parseWay(way); ok {
					f.pointsMutex.Lock()
					f.points = append(f.points, point)
					f.pointsMutex.Unlock()
				}
			})
		case *osm.Relation:
			rel := o
			pool.Submit(func() {
				if points := f.parseRelation(rel); len(points) > 0 {
					f.pointsMutex.Lock()
					f.points = append(f.points, points...)
					f.pointsMutex.Unlock()
				}
			})

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
	tags := node.TagMap()

	street := tags["addr:street"]
	housenumber := tags["addr:housenumber"]
	building := tags["building"]

	if housenumber != "" && street != "" && building != "" {
		city := tags["addr:city"]
		if city == "" {
			city = f.calcCity(node.Lat, node.Lon)
		}

		return kdbush.Point[geomodel.Info]{
			X: node.Lat,
			Y: node.Lon,
			Data: geomodel.Info{
				Name:        tags["name"],
				Street:      street,
				HouseNumber: housenumber,
				City:        city,
			},
		}, true
	}

	return kdbush.Point[geomodel.Info]{}, false
}

func (f *GeoGen) parseWay(way *osm.Way) (kdbush.Point[geomodel.Info], bool) {
	tags := way.TagMap()
	street := tags["addr:street"]
	housenumber := tags["addr:housenumber"]
	building := tags["building"]

	if housenumber != "" && street != "" && building != "" {
		lat, lon := f.calcWayCenter(way)

		city := tags["addr:city"]
		if city == "" {
			city = f.calcCity(lat, lon)
		}

		return kdbush.Point[geomodel.Info]{
			X: lat,
			Y: lon,
			Data: geomodel.Info{
				Name:        tags["name"],
				Street:      street,
				HouseNumber: housenumber,
				City:        city,
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
		var lat, lon float64
		if tags["type"] == "multipolygon" {
			mpoly, err := f.buildPolygon(rel.Members)
			if err != nil {
				logrus.Errorf("Error building polygon: %s", err.Error())
				//return kdbush.Point[geocoder.Info]{}, false
			}
			if mpoly == nil && len(mpoly) == 0 {
				logrus.Errorf("Empty polygon: %s", err.Error())
				//return kdbush.Point[geocoder.Info]{}, false
			}

			//lat, lon = Polylabel(mpoly[0][0], 0.00001)

			for _, poly := range mpoly {
				p, _ := planar.CentroidArea(poly)
				lat, lon = p[0], p[1]
				city := tags["addr:city"]
				if city == "" {
					city = f.calcCity(lat, lon)
				}

				points = append(points, kdbush.Point[geomodel.Info]{
					X: lat,
					Y: lon,
					Data: geomodel.Info{
						Name:        tags["name"],
						Street:      street,
						HouseNumber: housenumber,
						City:        city,
					},
				})
			}

		}
	}

	if tags["route"] == "road" && tags["type"] == "route" && strings.Contains(tags["network"], "national") {
		return f.parseRelationHighway(rel, tags)
	}

	return points
}

func (f *GeoGen) parseRelationHighway(rel *osm.Relation, tags map[string]string) []kdbush.Point[geomodel.Info] {
	points := []kdbush.Point[geomodel.Info]{}

	for _, m := range rel.Members {
		if m.Type != osm.TypeWay {
			continue
		}

		name := tags["name"]
		street := tags["ref"]

		if way, ok := f.wayCache.Get(m.Ref); ok {
			for _, point := range orb.LineString(way) {

				points = append(points, kdbush.Point[geomodel.Info]{
					X: point[0],
					Y: point[1],
					Data: geomodel.Info{
						Name:   name,
						Street: street,
						City:   f.calcCity(point[0], point[1]),
					},
				})
			}
		}
	}
	return points
}
