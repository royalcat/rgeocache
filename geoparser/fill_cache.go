package geoparser

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"sync"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/paulmach/osm"
	"github.com/sourcegraph/conc/pool"
)

func (f *GeoGen) fillRelCache() error {
	pool := pool.New().WithMaxGoroutines(f.config.Threads)
	defer pool.Wait()

	for rel, err := range iterWithProgress(f.osmdb.IterRelations(), int(f.osmdb.CountRelations()), "3/5 filling relations cache") {
		if err != nil {
			return err
		}
		pool.Go(func() {
			f.cacheRel(rel)
		})
	}

	return nil
}

func (f *GeoGen) parseDatabase() error {
	pool := pool.New().WithMaxGoroutines(f.config.Threads)

	var parseErr error
	consume := func(source iter.Seq2[osm.Object, error], total int, name string) {
		if parseErr != nil {
			return
		}
		// Assign claim sequence in the single-threaded consume loop, not in
		// the worker, so a shared node always canonicalizes to the same way
		// regardless of goroutine scheduling.
		var waySeq uint32
		for obj, err := range iterWithProgress(source, total, name) {
			if err != nil {
				parseErr = err
				return
			}
			seq := waySeq
			if _, ok := obj.(*osm.Way); ok {
				waySeq++
				seq = waySeq
			}
			pool.Go(func() {
				f.parseObject(obj, seq)
			})
		}
	}

	// Nodes and ways have to be read together: while the node iterator keeps
	// the OSM window cache warm, way-node lookups hit it. Reading nodes first
	// and ways afterwards turns every way-node lookup into a block decode
	// (measured ~14x slower on the post-CIS input).
	nodesWays := iterConcurrently(
		castIterToObject(f.osmdb.IterNodes()),
		castIterToObject(f.osmdb.IterWays()),
	)
	consume(nodesWays, int(f.osmdb.CountNodes()+f.osmdb.CountWays()), "4/5 generating nodes and ways")

	// Relations are a separate pass. Running three sources concurrently made
	// the OSM window cache drop relation windows under collector backpressure;
	// the relation set is small enough that a dedicated pass costs little.
	consume(castIterToObject(f.osmdb.IterRelations()), int(f.osmdb.CountRelations()), "5/5 generating relations")

	pool.Wait()
	if parseErr != nil {
		return parseErr
	}

	fmt.Printf("Seen nodes: %d (index %d)\n", f.seenNodes.Load(), f.osmdb.CountNodes())
	fmt.Printf("Seen ways: %d (index %d)\n", f.seenWays.Load(), f.osmdb.CountWays())
	fmt.Printf("Seen relations: %d (index %d)\n", f.seenRelations.Load(), f.osmdb.CountRelations())
	fmt.Printf("Duplicate way parse: %d\n", f.parsedWaysDupes.Load())
	fmt.Printf("Duplicate relation parse: %d\n", f.parsedRelationsDupes.Load())

	return nil
}

// iterConcurrently merges several object iterators into one sequence.
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
