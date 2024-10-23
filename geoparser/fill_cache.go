package geoparser

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/cheggaaa/pb/v3/termutil"
	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
	"github.com/sourcegraph/conc/pool"
)

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
	// if f.nodeCache != nil {
	// 	f.nodeCache.Close()
	// }
	// if f.wayCache != nil {
	// 	f.wayCache.Close()
	// }

	if f.placeCache != nil {
		f.placeCache.Close()
	}

	if f.localizationCache != nil {
		f.localizationCache.Close()
	}
}
