package geoparser

import (
	"cmp"
	"context"
	"os"
	"slices"
	"time"

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

	f.parsedPoints = uniqueGeoPoints(f.parsedPoints)

	f.log.Info("Saving points to file", "count", len(f.parsedPoints))

	points := make([]kdbush.Point[geomodel.Info], 0, len(f.parsedPoints))
	for _, point := range f.parsedPoints {
		points = append(points, kdbush.Point[geomodel.Info]{
			X:    point.X(),
			Y:    point.Y(),
			Data: point.Info,
		})
	}

	meta := cachesaver.Metadata{
		Version:     f.config.Version,
		Locale:      f.config.PreferredLocalization,
		DateCreated: time.Now(),
	}
	if err = cachesaver.Save(points, meta, dataFile); err != nil {
		return err
	}

	return dataFile.Close()
}

func uniqueGeoPoints(points []geoPoint) []geoPoint {
	// go requires strict weak ordering but struct not directry comparable, so we use a Cantor pairing function for cooridates with fixed precision
	const precisionAmplifier = 1_000_000
	cantorPairFunc := func(xf, yf float64) int64 {
		x := int64(xf * precisionAmplifier)
		y := int64(yf * precisionAmplifier)

		return (x+y)*(x+y+1)/2 + y
	}
	slices.SortFunc(points, func(a, b geoPoint) int {
		if a == b {
			return 0
		}
		return cmp.Compare(cantorPairFunc(a.X(), a.Y()), cantorPairFunc(b.X(), b.Y()))
	})
	return slices.Compact(points)
}
