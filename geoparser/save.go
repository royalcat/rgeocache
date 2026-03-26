package geoparser

import (
	"cmp"
	"slices"
	"time"
	"unique"

	"github.com/royalcat/rgeocache/cachesaver"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

func (f *GeoGen) saveWorker() error {
	points := func(yield func(cachemodel.Point) bool) {
		for point := range f.parsedPoints {
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
		<-f.parsingDone

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

	return cachesaver.Save(points, zones, meta, f.output)
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
