package geoparser

import (
	"context"
	"os"

	"github.com/royalcat/rgeocache/cachesaver"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
)

// TODO
// https://download.geofabrik.de/russia-latest.osm.pbf
func DownloadOsm(ctx context.Context, name string) {

}

func (f *GeoGen) SavePointsToFile(file string) error {
	dataFile, err := os.Create(file)

	if err != nil {
		return err
	}

	f.parsedPointsMu.Lock()
	defer f.parsedPointsMu.Unlock()

	f.parsedPoints = uniq(f.parsedPoints)

	points := make([]kdbush.Point[geomodel.Info], 0, len(f.parsedPoints))
	for _, point := range f.parsedPoints {
		points = append(points, kdbush.Point[geomodel.Info]{
			X:    point.X(),
			Y:    point.Y(),
			Data: point.Info,
		})
	}

	if err := cachesaver.Save(points, dataFile); err != nil {
		return err
	}

	return dataFile.Close()
}

func uniq[T comparable](s []T) []T {
	seen := make(map[T]struct{}, len(s))
	j := 0
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		s[j] = v
		j++
	}
	return s[:j]
}
