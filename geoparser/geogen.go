package geoparser

import (
	"context"
	"log/slog"
	"os"
	"sync"

	"github.com/puzpuzpuz/xsync/v3"
	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/bordertree"
	"golang.org/x/exp/mmap"
)

type GeoGen struct {
	threads               int
	preferredLocalization string

	placeIndex  *bordertree.BorderTree[string]
	regionIndex *bordertree.BorderTree[string]

	localizationCache *xsync.MapOf[string, string]

	osmdb *osmpbfdb.DB

	parsedPointsMu sync.Mutex
	parsedPoints   []geoPoint

	log *slog.Logger
}

func NewGeoGen(threads int, preferredLocalization string) (*GeoGen, error) {
	f := &GeoGen{
		placeIndex:        bordertree.NewBorderTree[string](),
		regionIndex:       bordertree.NewBorderTree[string](),
		localizationCache: xsync.NewMapOf[string, string](),

		threads:               threads,
		preferredLocalization: preferredLocalization,

		parsedPoints: []geoPoint{},

		log: slog.Default(),
	}

	err := f.ResetCache()

	return f, err
}

func (f *GeoGen) ResetCache() error {
	f.placeIndex = bordertree.NewBorderTree[string]()
	f.regionIndex = bordertree.NewBorderTree[string]()
	f.localizationCache = xsync.NewMapOf[string, string]()

	return nil
}

func (f *GeoGen) ParseOSMFile(ctx context.Context, input string) error {
	{
		file, err := mmap.Open(input)
		if err != nil {
			return err
		}
		defer file.Close()

		f.osmdb, err = osmpbfdb.OpenDB(file, osmpbfdb.Config{})
		if err != nil {
			return err
		}
	}

	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()

	err = f.fillRelCache(ctx, file)
	if err != nil {
		return err
	}

	err = f.parseDatabase(ctx, file)
	if err != nil {
		return err
	}

	return nil
}
