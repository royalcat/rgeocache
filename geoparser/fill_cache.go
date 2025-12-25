package geoparser

import (
	"context"
	"iter"
	"log/slog"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/paulmach/osm"
	"github.com/royalcat/osmpbfdb"
	"github.com/sourcegraph/conc/pool"
)

func (f *GeoGen) fillRelCache(ctx context.Context, db osmpbfdb.OsmDB) error {
	ctx, span := tracer.Start(ctx, "fillRelCache")
	defer span.End()

	pool := pool.New().WithMaxGoroutines(f.config.Threads)
	defer pool.Wait()

	for rel, err := range iterWithProgress(db.IterRelations(), int(db.CountRelations()), "3/4 filling relations cache") {
		if err != nil {
			return err
		}
		pool.Go(func() {
			f.cacheRel(ctx, rel)
		})
	}

	return nil
}

func (f *GeoGen) parseDatabase(ctx context.Context, db osmpbfdb.OsmDB) error {
	ctx, span := tracer.Start(ctx, "parseDatabase")
	defer span.End()

	pool := pool.New().WithMaxGoroutines(f.config.Threads)
	defer pool.Wait()

	objectsCount := int(db.CountNodes() + db.CountWays() + db.CountRelations())
	objectsIter := iterConcurrently(
		castIterToObject(db.IterNodes()),
		castIterToObject(db.IterWays()),
		castIterToObject(db.IterRelations()),
	)

	for obj, err := range iterWithProgress(objectsIter, objectsCount, "4/4 generating database") {
		if err != nil {
			return err
		}
		pool.Go(func() {
			f.parseObject(ctx, obj)
		})
	}

	return nil
}

func iterWithProgress[T any](source iter.Seq2[T, error], total int, name string) iter.Seq2[T, error] {
	bar := pb.New(total)
	bar.Set(pb.Terminal, false)
	bar.Set("prefix", name)
	bar.SetRefreshRate(time.Second)
	bar.SetTemplate(pb.Full)
	bar.SetWidth(80)
	bar.SetWriter(&slogWriter{
		logger: slog.Default(),
	})

	return func(yield func(T, error) bool) {
		bar.Start()
		defer bar.Finish()

		for item, err := range source {
			if !yield(item, err) {
				return
			}
			bar.Increment()
		}
	}
}

func iterConcurrently[T any](sources ...iter.Seq2[T, error]) iter.Seq2[T, error] {
	type elem struct {
		item T
		err  error
	}

	out := make(chan elem, len(sources))
	var wg sync.WaitGroup
	for _, source := range sources {
		wg.Add(1)
		go func(source iter.Seq2[T, error]) {
			defer wg.Done()
			for item, err := range source {
				out <- elem{item: item, err: err}
			}
		}(source)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return func(yield func(T, error) bool) {
		for val := range out {
			if !yield(val.item, val.err) {
				return
			}
		}
	}
}

func castIterToObject[I osm.Object](source iter.Seq2[I, error]) iter.Seq2[osm.Object, error] {
	return func(yield func(osm.Object, error) bool) {
		for item, err := range source {
			if !yield(item, err) {
				return
			}
		}
	}
}

type slogWriter struct {
	level  slog.Level
	logger *slog.Logger
}

func (w *slogWriter) Write(p []byte) (n int, err error) {
	w.logger.Log(context.Background(), w.level, string(p))
	return len(p), nil
}
