package geoparser

import (
	"context"
	"os"
	"path"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/cheggaaa/pb/v3/termutil"
	"github.com/paulmach/orb"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kv"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"golang.org/x/exp/constraints"

	"github.com/sourcegraph/conc/pool"

	"github.com/sirupsen/logrus"
)

type GeoGen struct {
	cachePath             string
	threads               int
	preferredLocalization string

	nodeCache kv.KVS[osm.NodeID, cachePoint]
	wayCache  kv.KVS[osm.WayID, cacheWay]

	placeCache        kv.KVS[osm.RelationID, cachePlace]
	localizationCache kv.KVS[string, string]

	points kv.KVS[orb.Point, geomodel.Info]

	log *logrus.Logger
}

func NewGeoGen(cachePath string, threads int, preferredLocalization string) (*GeoGen, error) {
	f := &GeoGen{
		cachePath: cachePath,

		threads:               threads,
		preferredLocalization: preferredLocalization,

		points: kv.NewMutexMap[orb.Point, geomodel.Info](),

		log: logrus.New(),
	}

	logrus.Info("Opening cache database")
	err := f.OpenCache()

	return f, err
}

func (f *GeoGen) OpenCache() error {
	var err error
	f.nodeCache, err = newCache[osm.NodeID, cachePoint](f.cachePath, "nodes")
	if err != nil {
		return err
	}
	f.wayCache, err = newCache[osm.WayID, cacheWay](f.cachePath, "ways")
	if err != nil {
		return err
	}
	f.placeCache = newMemoryCache[osm.RelationID, cachePlace]()
	f.localizationCache = newMemoryCache[string, string]()
	return err
}

func (f *GeoGen) ParseOSMFile(ctx context.Context, base string) error {
	err := f.fillCache(ctx, base)
	if err != nil {
		return err
	}

	err = f.parseDatabase(ctx, base)
	if err != nil {
		return err
	}

	return nil
}

func (f *GeoGen) fillCache(ctx context.Context, base string) error {
	err := f.fillNodeCache(ctx, base)
	if err != nil {
		return err
	}
	err = f.fillWayRelCache(ctx, base)
	if err != nil {
		return err
	}

	f.nodeCache = nil
	return nil
}

func (f *GeoGen) fillWayRelCache(ctx context.Context, base string) error {
	// log := f.log.WithField("base", base)

	file, err := os.Open(base)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, _ := file.Stat()

	scanner := osmpbf.New(ctx, file, f.threads)
	defer scanner.Close()
	scanner.SkipNodes = true
	return scanWithProgress(scanner, stat.Size(), "2/3 filling ways and relations cache", func(object osm.Object) bool {
		switch object := object.(type) {
		case *osm.Way:
			f.cacheWay(object)
		case *osm.Relation:
			f.cacheRel(object)
		}
		return true
	})
}

func (f *GeoGen) fillNodeCache(ctx context.Context, base string) error {
	log := f.log.WithField("base", base)

	file, err := os.Open(base)
	if err != nil {
		return err
	}
	defer file.Close()

	stat, _ := file.Stat()

	scanner := osmpbf.New(ctx, file, f.threads)
	defer scanner.Close()
	scanner.SkipWays = true
	scanner.SkipRelations = true
	return scanWithProgress(scanner, stat.Size(), "1/3 filling node cache", func(object osm.Object) bool {
		node, ok := object.(*osm.Node)
		if !ok {
			log.Error("Object does not type of node")
		}
		f.cacheNode(node)
		return true
	})
}

func scanWithProgress(scanner *osmpbf.Scanner, size int64, name string, it func(osm.Object) bool) error {
	bar := pb.Start64(size)
	bar.Set("prefix", name)
	bar.Set(pb.Bytes, true)
	bar.SetRefreshRate(time.Second * 5)
	if w, err := termutil.TerminalWidth(); w == 0 || err != nil {
		bar.SetTemplateString(`{{with string . "prefix"}}{{.}} {{end}}{{counters . }} {{bar . }} {{percent . }} {{speed . }} {{rtime . "ETA %s"}}{{with string . "suffix"}} {{.}}{{end}}` + "\n")
	}

	for scanner.Scan() {
		bar.SetCurrent(scanner.FullyScannedBytes())
		it(scanner.Object())
	}
	bar.Finish()

	return scanner.Err()
}

func (f *GeoGen) parseDatabase(ctx context.Context, base string) error {
	file, err := os.Open(base)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, _ := file.Stat()

	// The third parameter is the number of parallel decoders to use.
	scanner := osmpbf.New(ctx, file, f.threads)
	defer scanner.Close()

	bar := pb.Start64(stat.Size())
	bar.Set("prefix", "3/3 generating database")
	bar.Set(pb.Bytes, true)
	bar.SetRefreshRate(time.Second)
	if w, err := termutil.TerminalWidth(); w == 0 || err != nil {
		bar.SetTemplateString(`{{with string . "prefix"}}{{.}} {{end}}{{counters . }} {{bar . }} {{percent . }} {{speed . }} {{rtime . "ETA %s"}}{{with string . "suffix"}} {{.}}{{end}}` + "\n")
	}

	pool := pool.New().WithMaxGoroutines(f.threads)
	for scanner.Scan() {
		bar.SetCurrent(scanner.FullyScannedBytes())
		object := scanner.Object()
		pool.Go(func() {
			f.parseObject(object)
		})
	}
	pool.Wait()

	bar.Finish()
	if err := scanner.Err(); err != nil {
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

	if f.localizationCache != nil {
		f.localizationCache.Close()
	}
}

func newCache[K ~int64, V kv.ValueBytes[V]](base, name string) (kv.KVS[K, V], error) {
	if base == "memory" {
		return newMemoryCache[K, V](), nil
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
		return kv.NewLevelDbKV[K, V](cacheDb), nil
	}

}

func newMemoryCache[K constraints.Ordered, V any]() kv.KVS[K, V] {
	return kv.NewMutexMap[K, V]()
}
