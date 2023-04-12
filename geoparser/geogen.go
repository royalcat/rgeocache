package geoparser

import (
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

	cityCache kv.KVS[int64, cacheCity]

	nodeCache kv.KVS[int64, cachePoint]
	wayCache  kv.KVS[int64, cacheWay]

	genNodeCache bool

	points      []kdbush.Point[geomodel.Info]
	pointsMutex sync.Mutex

	threads int

	log *logrus.Logger
}

func NewGeoGen(cachePath string, needToGenCache bool, threads int) (*GeoGen, error) {
	f := &GeoGen{
		CachePath:    cachePath,
		genNodeCache: needToGenCache,

		threads: threads,

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
	f.cityCache = kv.NewMap[int64, cacheCity]()
	return err
}

func (f *GeoGen) Close() {
	f.nodeCache.Close()
	f.cityCache.Close()
	f.wayCache.Close()
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
		return kv.NewLevelDbKV[int64, V](cacheDb), nil
	}

}
