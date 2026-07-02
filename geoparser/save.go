package geoparser

import (
	"cmp"
	"fmt"
	"iter"
	"slices"
	"sync"
	"time"
	"unique"

	"github.com/royalcat/rgeocache/cachesaver"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"golang.org/x/sync/errgroup"
)

func (f *GeoGen) saveWorker(outputs []ParseOutput) error {
	points := func(yield func(cachemodel.Point) bool) {
		for point := range f.parsedPoints {
			if !yield(cachesaver.Point{
				X: point.X(),
				Y: point.Y(),
				Data: cachemodel.Info{
					Name:        unique.Make(point.Name),
					Street:      point.Street,
					HouseNumber: point.HouseNumber,
					City:        point.City,
					Region:      point.Region,
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

	pointsTee := Tee(points, len(outputs), 1)
	zonesTee := Tee(zones, len(outputs), 1)

	var wg errgroup.Group
	for i, output := range outputs {
		switch output.Format {
		case "v1":
			wg.Go(func() error {
				return cachesaver.SaveV1(pointsTee[i], zonesTee[i], meta, output.Writer)
			})
		case "v2":
			wg.Go(func() error {
				return cachesaver.SaveV2(pointsTee[i], zonesTee[i], meta, output.Writer)
			})
		default:
			return fmt.Errorf("unsupported format: %s", output.Format)
		}
	}

	return wg.Wait()
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

// Tee duplicates a source iterator into 'n' independent copies.
func Tee[V any](src iter.Seq[V], n int, bufferSize int) []iter.Seq[V] {
	// A slice of channels to hold data for each copy
	chs := make([]chan V, n)
	for i := range chs {
		chs[i] = make(chan V, bufferSize)
	}

	// Fan out data from the source iterator
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer func() {
			for _, ch := range chs {
				close(ch) // Close all channels when src is exhausted
			}
		}()

		for v := range src {
			for _, ch := range chs {
				ch <- v
			}
		}
	}()

	// Create 'n' independent iterators from the channels
	iters := make([]iter.Seq[V], n)
	for i := range chs {
		ch := chs[i]
		iters[i] = func(yield func(V) bool) {
			for {
				v, ok := <-ch
				if !ok {
					break // Channel closed, this specific iterator exits
				}
				if !yield(v) {
					// Loop body exited early (break), disconnect this channel
					// by draining it to prevent the source goroutine from blocking.
					go func() {
						for range ch {
						}
					}()
					break
				}
			}
		}
	}

	return iters
}
