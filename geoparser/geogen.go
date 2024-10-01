package geoparser

import (
	"context"
	"os"
	"path"
	"runtime"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/cheggaaa/pb/v3/termutil"
	"github.com/paulmach/osm/osmpbf"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/kdbush"
	"github.com/royalcat/rgeocache/kv"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"

	"github.com/sirupsen/logrus"
)

type GeoGen struct {
	cachePath string

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
		cachePath: cachePath,

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
	f.nodeCache, err = newCache[cachePoint](f.cachePath, "nodes")
	if err != nil {
		return err
	}
	f.wayCache, err = newCache[cacheWay](f.cachePath, "ways")
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

	err = f.parseDatabase(ctx, base)
	if err != nil {
		return err
	}

	return nil
}

func (f *GeoGen) fillCache(ctx context.Context, base string) error {
	file, err := os.Open(base)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, _ := file.Stat()

	// The third parameter is the number of parallel decoders to use.
	scanner := osmpbf.New(ctx, file, runtime.GOMAXPROCS(-1)-1)
	defer scanner.Close()

	bar := pb.Start64(stat.Size())
	bar.Set("prefix", "1/2 filling cache")
	bar.Set(pb.Bytes, true)
	bar.SetRefreshRate(time.Second)
	if w, err := termutil.TerminalWidth(); w == 0 || err != nil {
		bar.SetTemplateString(`{{with string . "prefix"}}{{.}} {{end}}{{counters . }} {{bar . }} {{percent . }} {{speed . }} {{rtime . "ETA %s"}}{{with string . "suffix"}} {{.}}{{end}}` + "\n")
	}

	for scanner.Scan() {
		bar.SetCurrent(scanner.FullyScannedBytes())
		f.cacheObject(scanner.Object())
	}
	bar.Finish()

	if err := scanner.Err(); err != nil {
		return err
	}

	return nil
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
	bar.Set("prefix", "2/2 generating database")
	bar.Set(pb.Bytes, true)
	bar.SetRefreshRate(time.Second)
	if w, err := termutil.TerminalWidth(); w == 0 || err != nil {
		bar.SetTemplateString(`{{with string . "prefix"}}{{.}} {{end}}{{counters . }} {{bar . }} {{percent . }} {{speed . }} {{rtime . "ETA %s"}}{{with string . "suffix"}} {{.}}{{end}}` + "\n")
	}

	for scanner.Scan() {
		bar.SetCurrent(scanner.FullyScannedBytes())
		f.parseObject(scanner.Object())
	}
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
