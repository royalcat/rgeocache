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

// Generation is deterministic: the output depends only on the input and the
// configuration, never on thread count or scheduling. Workers compute results
// concurrently, but every result goes through a sequencer that releases it in
// input order, and every decision that depends on "who came first" (duplicate
// ids, claim sequence numbers) is taken in the single-threaded loop that reads
// the input.

// sequencerWindow is the number of results one stage may have in flight.
func (f *GeoGen) sequencerWindow() int {
	return max(256, 64*f.config.Threads)
}

func (f *GeoGen) fillRelCache() error {
	seq := newSequencer(f.sequencerWindow(), f.applyRelCache)
	workers := pool.New().WithMaxGoroutines(f.config.Threads)

	for rel, err := range iterWithProgress(f.osmdb.IterRelations(), int(f.osmdb.CountRelations()), "3/5 filling relations cache") {
		if err != nil {
			workers.Wait()
			return err
		}
		seq.submit(workers, func() relCacheResult {
			return f.cacheRel(rel)
		})
	}

	workers.Wait()
	if f.localizationConflicts > 0 {
		f.log.Warn("Conflicting name localizations, kept the first in relation order",
			"conflicts", f.localizationConflicts)
	}
	return nil
}

// emitter returns a sequencer sink that forwards batches to ch.
func emitter(ch chan<- parseItem) func([]parseItem) {
	return func(items []parseItem) {
		for _, item := range items {
			if !item.empty() {
				ch <- item
			}
		}
	}
}

// parseDatabase reads nodes, ways and relations and feeds the two collectors:
//
//   - wayItems receives the ways, then the relations, each in input order;
//   - nodeItems receives the address nodes in input order.
//
// The streams are separate because nodes and ways are read concurrently (see
// below): their interleaving is not deterministic, so they must not share an
// output order. The nodes are appended after everything else (see
// ParseOSMData); they never take part in the road graph, so that is safe.
func (f *GeoGen) parseDatabase(wayItems, nodeItems chan<- parseItem) error {
	window := f.sequencerWindow()

	nodeSeq := newSequencer(window, emitter(nodeItems))
	waySeq := newSequencer(window, emitter(wayItems))
	workers := pool.New().WithMaxGoroutines(f.config.Threads)

	// Nodes and ways have to be read together: while the node iterator keeps
	// the OSM window cache warm, way-node lookups hit it. Reading nodes first
	// and ways afterwards turns every way-node lookup into a block decode
	// (measured ~14x slower on the post-CIS input).
	nodesWays := iterConcurrently(
		castIterToObject(f.osmdb.IterNodes()),
		castIterToObject(f.osmdb.IterWays()),
	)

	// The claim sequence number picks which way's attributes win at a shared
	// node. It is assigned here, in the single-threaded loop, to the ways that
	// survive the duplicate check, so it follows the way input order.
	var claimSeq uint32
	var parseErr error
	for obj, err := range iterWithProgress(nodesWays, int(f.osmdb.CountNodes()+f.osmdb.CountWays()), "4/5 generating nodes and ways") {
		if err != nil {
			parseErr = err
			break
		}
		switch o := obj.(type) {
		case *osm.Node:
			f.seenNodes.Add(1)
			// Only address nodes produce output. Filtering here keeps the
			// hundreds of millions of plain nodes out of the worker pool and
			// the sequencer.
			if !isBuilding(o.Tags) {
				continue
			}
			nodeSeq.submit(workers, func() []parseItem { return f.parseObject(o, 0) })
		case *osm.Way:
			f.seenWays.Add(1)
			if !f.parsedWays.Add(int64(o.ID)) {
				f.parsedWaysDupes.Add(1)
				continue
			}
			claimSeq++
			claim := claimSeq
			waySeq.submit(workers, func() []parseItem { return f.parseObject(o, claim) })
		}
	}

	// Barrier: relation parsing consults the set of parsed ways, which must be
	// final (and every way batch emitted) before the first relation starts.
	workers.Wait()
	if parseErr != nil {
		return parseErr
	}

	// Relations are a separate pass. Running three sources concurrently made
	// the OSM window cache drop relation windows under collector backpressure;
	// the relation set is small enough that a dedicated pass costs little.
	//
	// A route relation can pull in a way that no other relation has emitted
	// yet; two relations can share one. The first in input order keeps it, which
	// the ordered sink below enforces.
	relWays := map[osm.WayID]struct{}{}
	relSink := emitter(wayItems)
	relSeq := newSequencer(window, func(items []parseItem) {
		kept := items[:0:0]
		for _, item := range items {
			if item.WayID != 0 {
				if _, dup := relWays[item.WayID]; dup {
					continue
				}
				relWays[item.WayID] = struct{}{}
			}
			kept = append(kept, item)
		}
		relSink(kept)
	})
	workers = pool.New().WithMaxGoroutines(f.config.Threads)
	for rel, err := range iterWithProgress(f.osmdb.IterRelations(), int(f.osmdb.CountRelations()), "5/5 generating relations") {
		if err != nil {
			parseErr = err
			break
		}
		f.seenRelations.Add(1)
		if !f.parsedRelations.Add(int64(rel.ID)) {
			f.parsedRelationsDupes.Add(1)
			continue
		}
		relSeq.submit(workers, func() []parseItem { return f.parseObject(rel, 0) })
	}

	workers.Wait()
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
