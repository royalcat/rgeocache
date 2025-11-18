package geoparser

import (
	"iter"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/paulmach/osm"
	"github.com/royalcat/osmpbfdb"
	"github.com/sourcegraph/conc/pool"
)

func (f *GeoGen) fillRelCache(db osmpbfdb.OsmDB) error {
	pool := pool.New().WithMaxGoroutines(f.threads)
	defer pool.Wait()

	for rel, err := range iterWithProgress(db.IterRelations(), int(db.CountRelations()), "3/4 filling relations cache") {
		if err != nil {
			return err
		}
		pool.Go(func() {
			f.cacheRel(rel)
		})
	}

	return nil
}

func (f *GeoGen) parseDatabase(db osmpbfdb.OsmDB) error {
	pool := pool.New().WithMaxGoroutines(f.threads)
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
			f.parseObject(obj)
		})
	}

	return nil
}

func iterWithProgress[T any](source iter.Seq2[T, error], total int, name string) iter.Seq2[T, error] {
	bar := pb.StartNew(total)
	bar.Set("prefix", name)
	bar.SetRefreshRate(time.Second * 5)
	bar.SetTemplate(pb.Full)

	return func(yield func(T, error) bool) {
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
