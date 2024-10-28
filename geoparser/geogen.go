package geoparser

import (
	"context"
	"os"
	"path"

	"github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"
	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kv"
	"github.com/royalcat/rgeocache/osmpbfdb"
	"golang.org/x/exp/constraints"
	"golang.org/x/exp/mmap"

	"github.com/sirupsen/logrus"
)

type GeoGen struct {
	cachePath             string
	threads               int
	preferredLocalization string

	// nodeCache  kv.KVS[osm.NodeID, cachePoint]
	// wayCache   kv.KVS[osm.WayID, cacheWay]
	placeCache kv.KVS[osm.RelationID, cachePlace]

	osmdb *osmpbfdb.DB

	localizationCache kv.KVS[string, string]

	points kv.KVS[orb.Point, geomodel.Info]

	log *logrus.Entry
}

func NewGeoGen(cachePath string, threads int, preferredLocalization string) (*GeoGen, error) {
	f := &GeoGen{
		cachePath: cachePath,

		threads:               threads,
		preferredLocalization: preferredLocalization,

		points: kv.NewMutexMap[orb.Point, geomodel.Info](),

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
	f.placeCache = newMemoryCache[osm.RelationID, cachePlace]()
	f.localizationCache = newMemoryCache[string, string]()

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

	f.placeCache.Flush()
	f.localizationCache.Flush()

	err = f.parseDatabase(ctx, file)
	if err != nil {
		return err
	}

	return nil
}

// func (f *GeoGen) fillNodeCache(ctx context.Context, file *os.File) error {
// 	log := f.log.WithField("input", file.Name())
// 	_, err := file.Seek(0, io.SeekStart)
// 	if err != nil {
// 		return err
// 	}
// 	stat, err := file.Stat()
// 	if err != nil {
// 		return err
// 	}
// 	scanner := osmpbf.New(ctx, file, f.threads)
// 	defer scanner.Close()
// 	scanner.SkipWays = true
// 	scanner.SkipRelations = true

// 	pool := pool.New().WithMaxGoroutines(f.threads)
// 	defer pool.Wait()

// 	return scanWithProgress(scanner, stat.Size(), "1/4 filling node cache", func(object osm.Object) bool {
// 		node, ok := object.(*osm.Node)
// 		if !ok {
// 			log.Error("Object does not type of node")
// 		}

// 		pool.Go(func() {
// 			f.cacheNode(node)
// 		})

// 		return true
// 	})
// }

// func (f *GeoGen) fillWayCache(ctx context.Context, file *os.File) error {
// 	log := f.log.WithField("input", file.Name())
// 	_, err := file.Seek(0, io.SeekStart)
// 	if err != nil {
// 		return err
// 	}
// 	stat, err := file.Stat()
// 	if err != nil {
// 		return err
// 	}

// 	scanner := osmpbf.New(ctx, file, f.threads)
// 	defer scanner.Close()
// 	scanner.SkipNodes = true
// 	scanner.SkipRelations = true

// 	pool := pool.New().WithMaxGoroutines(f.threads)
// 	defer pool.Wait()

// 	return scanWithProgress(scanner, stat.Size(), "2/4 filling ways cache", func(object osm.Object) bool {
// 		way, ok := object.(*osm.Way)
// 		if !ok {
// 			log.Error("Object does not type of node")
// 		}

// 		pool.Go(func() {
// 			f.cacheWay(way)
// 		})

// 		return true
// 	})
// }

func newCache[K ~int64, V kv.ValueBytes[V]](basePath, name string) (kv.KVS[K, V], error) {
	if basePath == "memory" {
		return newMemoryCache[K, V](), nil
	} else {
		opts := badger.DefaultOptions(path.Join(basePath, name)).
			WithCompactL0OnClose(true).
			WithCompression(options.ZSTD).
			WithBlockSize(128 * 1024).
			WithBlockCacheSize(3 * 1024 * 1024).
			WithVLogPercentile(0.90)
		cacheDb, err := badger.Open(opts)
		if err != nil {
			return nil, err
		}
		return kv.NewBadgerKVS[K, V](cacheDb), nil
		// err := os.MkdirAll(base, 0755)
		// if err != nil {
		// 	return nil, err
		// }

		// file, err := os.Create(path.Join(base, name))
		// if err != nil {
		// 	return nil, err
		// }

		// return kv.NewFileKV[K, V](file), nil
	}

}

func newMemoryCache[K constraints.Ordered, V any]() kv.KVS[K, V] {
	return kv.NewMutexMap[K, V]()
}
