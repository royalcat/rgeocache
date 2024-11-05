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
	cachePath             string
	threads               int
	preferredLocalization string

	placeCache        *xsync.MapOf[osm.RelationID, cachePlace]
	localizationCache *xsync.MapOf[string, string]

	osmdb *osmpbfdb.DB

	parsedPointsMu sync.Mutex
	parsedPoints   []geoPoint

	log *logrus.Entry
}

func NewGeoGen(cachePath string, threads int, preferredLocalization string) (*GeoGen, error) {
	f := &GeoGen{
		cachePath: cachePath,

		placeCache:        xsync.NewMapOf[osm.RelationID, cachePlace](),
		localizationCache: xsync.NewMapOf[string, string](),

		threads:               threads,
		preferredLocalization: preferredLocalization,

		parsedPoints: []geoPoint{},

		log: logrus.NewEntry(logrus.StandardLogger()),
	}

	logrus.Info("Opening cache database")
	err := f.OpenCache()

	return f, err
}

func (f *GeoGen) OpenCache() error {
	f.Close()
	// var err error

	// f.nodeCache = kv.NewPoints32MutexMap[osm.NodeID, cachePoint]()

	// f.wayCache, err = newCache[osm.WayID, cacheWay](f.cachePath, "ways")
	// if err != nil {
	// 	return err
	// }
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

	// err = f.fillNodeCache(ctx, file)
	// if err != nil {
	// 	return err
	// }

	// f.nodeCache.Flush()

	// err = f.fillWayCache(ctx, file)
	// if err != nil {
	// 	return err
	// }

	// f.nodeCache.Close()
	// f.nodeCache = nil

	// f.wayCache.Flush()

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
