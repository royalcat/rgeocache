package geoparser

import (
	"context"
	"path"
	"sync"

	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
	"github.com/royalcat/rgeocache/kv"

	"github.com/sirupsen/logrus"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

type GeoGen struct {
	CachePath string

	nodeCache kv.KVS[int64, cachePoint]
	wayCache  kv.KVS[int64, cacheWay]

	placeCache   kv.KVS[int64, cachePlace]
	highwayCache kv.KVS[int64, cacheHighway]

	points      []kdbush.Point[geomodel.Info]
	pointsMutex sync.Mutex

	threads               int
	preferredLocalization string

	log *logrus.Logger
}

func NewGeoGen(cachePath string, threads int, preferredLocalization string) (*GeoGen, error) {
	f := &GeoGen{
		CachePath: cachePath,

		threads:               threads,
		preferredLocalization: preferredLocalization,

		log: logrus.New(),
	}

	logrus.Info("Opening cache database")
	err := f.OpenCache()

	return f, err
}

func (f *GeoGen) OpenCache() error {
	var err error
	f.nodeCache, err = newCache[cachePoint](f.CachePath, "nodes")
	if err != nil {
		return err
	}
	f.wayCache, err = newCache[cacheWay](f.CachePath, "ways")
	if err != nil {
		return err
	}
	f.placeCache = kv.NewMap[int64, cachePlace]()
	f.highwayCache = kv.NewMap[int64, cacheHighway]()
	return err
}

func (f *GeoGen) ParseOSMFile(ctx context.Context, base string) error {
	err := f.fillCache(ctx, base)
	if err != nil {
		return err
	}

	err = f.parse(ctx, base)
	if err != nil {
		return err
	}

	return nil
}

func (f *GeoGen) Close() {
	if f.nodeCache != nil {
		f.nodeCache.Close()
	}
	if f.wayCache != nil {
		f.wayCache.Close()
	}

	if f.placeCache != nil {
		f.placeCache.Close()
	}
	if f.highwayCache != nil {
		f.placeCache.Close()
	}
}

func newCache[V kv.ValueBytes[V]](base, name string) (kv.KVS[int64, V], error) {
	if base == "memory" {
		return kv.NewMap[int64, V](), nil
	} else {
		var cacheDb *leveldb.DB
		var err error
		options := &opt.Options{
			NoSync:      true,
			WriteBuffer: 1024 * opt.MiB,
		}
		cacheDb, err = leveldb.OpenFile(path.Join(base, name), options)
		if err != nil {
			return nil, err
		}
		return kv.NewLevelDbKV[V](cacheDb), nil
	}

}
