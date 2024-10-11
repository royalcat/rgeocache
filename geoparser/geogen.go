package geoparser

import (
	"context"
	"io"
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

func (f *GeoGen) ParseOSMFile(ctx context.Context, input string) error {
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()

	err = f.fillNodeCache(ctx, file)
	if err != nil {
		return err
	}

	f.nodeCache.Flush()

	err = f.fillWayCache(ctx, file)
	if err != nil {
		return err
	}

	f.nodeCache.Close()
	f.nodeCache = nil

	f.wayCache.Flush()

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

func (f *GeoGen) fillNodeCache(ctx context.Context, file *os.File) error {
	log := f.log.WithField("input", file.Name())
	_, err := file.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	scanner := osmpbf.New(ctx, file, f.threads)
	defer scanner.Close()
	scanner.SkipWays = true
	scanner.SkipRelations = true

	pool := pool.New().WithMaxGoroutines(f.threads)
	defer pool.Wait()

	return scanWithProgress(scanner, stat.Size(), "1/4 filling node cache", func(object osm.Object) bool {
		node, ok := object.(*osm.Node)
		if !ok {
			log.Error("Object does not type of node")
		}

		pool.Go(func() {
			f.cacheNode(node)
		})

		return true
	})
}

func (f *GeoGen) fillWayCache(ctx context.Context, file *os.File) error {
	log := f.log.WithField("input", file.Name())
	_, err := file.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		return err
	}

	scanner := osmpbf.New(ctx, file, f.threads)
	defer scanner.Close()
	scanner.SkipNodes = true
	scanner.SkipRelations = true

	pool := pool.New().WithMaxGoroutines(f.threads)
	defer pool.Wait()

	return scanWithProgress(scanner, stat.Size(), "2/4 filling ways cache", func(object osm.Object) bool {
		way, ok := object.(*osm.Way)
		if !ok {
			log.Error("Object does not type of node")
		}

		pool.Go(func() {
			f.cacheWay(way)
		})

		return true
	})
}

func (f *GeoGen) fillRelCache(ctx context.Context, file *os.File) error {
	log := f.log.WithField("input", file.Name())
	_, err := file.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		return err
	}

	scanner := osmpbf.New(ctx, file, f.threads)
	defer scanner.Close()
	scanner.SkipNodes = true
	scanner.SkipWays = true

	pool := pool.New().WithMaxGoroutines(f.threads)
	defer pool.Wait()

	return scanWithProgress(scanner, stat.Size(), "3/4 filling relations cache", func(object osm.Object) bool {
		rel, ok := object.(*osm.Relation)
		if !ok {
			log.Error("Object does not type of relation")
		}

		pool.Go(func() {
			f.cacheRel(rel)
		})

		return true
	})
}

func (f *GeoGen) parseDatabase(ctx context.Context, file *os.File) error {
	// log := f.log.WithField("input", file.Name())
	_, err := file.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		return err
	}

	// The third parameter is the number of parallel decoders to use.
	scanner := osmpbf.New(ctx, file, f.threads)
	defer scanner.Close()

	pool := pool.New().WithMaxGoroutines(f.threads)
	defer pool.Wait()

	return scanWithProgress(scanner, stat.Size(), "4/4 generating database", func(object osm.Object) bool {
		pool.Go(func() {
			f.parseObject(object)
		})
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
		obj := scanner.Object()
		it(obj)
	}
	bar.Finish()

	return scanner.Err()
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
		// opts := badger.DefaultOptions(path.Join(base, name)).
		// 	WithCompactL0OnClose(true).
		// 	WithCompression(options.ZSTD).
		// 	WithBlockSize(128 * 1024).
		// 	WithBlockCacheSize(3 * 1024 * 1024).
		// 	WithVLogPercentile(0.90)
		// cacheDb, err := badger.Open(opts)
		// if err != nil {
		// 	return nil, err
		// }
		// return kv.NewBadgerKVS[K, V](cacheDb), nil
		err := os.MkdirAll(base, 0755)
		if err != nil {
			return nil, err
		}

		file, err := os.Create(path.Join(base, name))
		if err != nil {
			return nil, err
		}

		return kv.NewFileKV[K, V](file), nil
	}

}

func newMemoryCache[K constraints.Ordered, V any]() kv.KVS[K, V] {
	return kv.NewMutexMap[K, V]()
}
