package geoparser

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"time"
	"unique"

	"github.com/royalcat/rgeocache/cachesaver"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
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
	defer dataFile.Close()

	f.parsedPointsMu.Lock()
	defer f.parsedPointsMu.Unlock()

	f.parsedPoints = uniqueGeoPoints(f.parsedPoints)

	f.log.Info("Saving points to file", "count", len(f.parsedPoints))

	points := func(yield func(cachemodel.Point) bool) {
		for _, point := range f.parsedPoints {
			if !yield(cachesaver.Point{
				X: point.X(),
				Y: point.Y(),
				Data: cachemodel.Info{
					Name:        unique.Make(point.Name),
					Street:      unique.Make(point.Street),
					HouseNumber: unique.Make(point.HouseNumber),
					City:        unique.Make(point.City),
					Region:      unique.Make(point.Region),
					Weight:      point.Weight,
				},
			}) {
				return
			}
		}
	}

	zones := func(yield func(cachemodel.Zone) bool) {
		for _, zone := range f.regions {
			if !yield(cachesaver.Zone{
				Type:    cachemodel.ZoneRegion,
				Name:    unique.Make(zone.Name),
				Bounds:  zone.Bounds,
				Polygon: zone.Polygon,
			}) {
				return
			}
		}

		for _, zone := range f.countries {
			if !yield(cachesaver.Zone{
				Type:    cachemodel.ZoneCountry,
				Name:    unique.Make(zone.Name),
				Bounds:  zone.Bounds,
				Polygon: zone.Polygon,
			}) {
				return
			}
		}
	}

	meta := cachesaver.Metadata{
		Version:     f.config.Version,
		Locale:      f.config.PreferredLocalization,
		DateCreated: time.Now(),
	}
	if err = cachesaver.Save(points, zones, meta, dataFile); err != nil {
		return err
	}

	// Close the file so that all OS buffers are flushed, and we can safely reopen it for reading
	err = dataFile.Close()
	if err != nil {
		return fmt.Errorf("failed to close file after writing: %w", err)
	}

	analysisError := func() error {
		fRead, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("failed to open file for analysis: %w", err)
		}
		defer fRead.Close()

		err = cachesaver.PrintCacheSizeAnalysis(fRead)
		if err != nil {
			return fmt.Errorf("failed to analyze cache: %w", err)
		}
		return nil
	}()
	if analysisError != nil {
		f.log.Error("Failed to analyze cache", "error", analysisError)
	}

	return nil
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
