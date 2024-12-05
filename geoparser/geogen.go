package geoparser

import (
	"context"
	"os"
	"sync"

	"github.com/paulmach/osm"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/royalcat/rgeocache/osmpbfdb"
	"golang.org/x/exp/mmap"

	"github.com/sirupsen/logrus"
)

type GeoGen struct {
	threads               int
	preferredLocalization string

	placeCache        *xsync.MapOf[osm.RelationID, cachePlace]
	localizationCache *xsync.MapOf[string, string]

	osmdb *osmpbfdb.DB

	parsedPointsMu sync.Mutex
	parsedPoints   []geoPoint

	log *logrus.Entry
}

func NewGeoGen(threads int, preferredLocalization string) (*GeoGen, error) {
	f := &GeoGen{
		placeCache:        xsync.NewMapOf[osm.RelationID, cachePlace](),
		localizationCache: xsync.NewMapOf[string, string](),

		threads:               threads,
		preferredLocalization: preferredLocalization,

		parsedPoints: []geoPoint{},

		log: logrus.NewEntry(logrus.StandardLogger()),
	}

	err := f.ResetCache()

	return f, err
}

func (f *GeoGen) ResetCache() error {
	f.placeCache = xsync.NewMapOf[osm.RelationID, cachePlace]()
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

		f.osmdb, err = osmpbfdb.OpenDB(ctx, file)
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
