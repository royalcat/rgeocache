package geoparser

import (
	"sync"

	"github.com/sourcegraph/conc/pool"
)

// sequencer runs tasks concurrently but hands their results to emit strictly in
// submission order. This is what makes generation deterministic: workers finish
// in a scheduling-dependent order, but everything downstream (the spool, string
// ids, graph edges, zones, border-tree insertion) only ever sees the results in
// the order the OSM objects were read.
//
// It is used instead of conc's stream.Stream on purpose: Stream ties its
// reorder window to its goroutine count and funnels every task through a
// channel and a single callback goroutine. Matching this window while still
// capping compute at --threads needed a semaphore around it, and generation was
// still about 30% slower (London, 8 and 32 threads, byte-identical output).
//
// submit must be called from a single goroutine. emit is called with the
// sequencer's lock held, one result at a time, so it needs no locking of its
// own — but it must not call back into the sequencer.
//
// The window bounds the number of results that are submitted but not yet
// emitted. Without it, one slow early task would let every later result pile up
// in the reorder buffer while the pool keeps accepting work.
type sequencer[T any] struct {
	emit   func(T)
	window chan struct{}

	issued uint64 // next sequence number to hand out; submit goroutine only

	mu      sync.Mutex
	next    uint64 // next sequence number to emit
	pending map[uint64]T
}

func newSequencer[T any](window int, emit func(T)) *sequencer[T] {
	if window < 1 {
		window = 1
	}
	return &sequencer[T]{
		emit:    emit,
		window:  make(chan struct{}, window),
		pending: make(map[uint64]T),
	}
}

// submit reserves the next sequence number — blocking while the window is full
// — and runs fn on the pool. fn's result is emitted once every earlier task has
// been emitted. A panicking fn emits the zero value so the sequence cannot
// stall; the pool re-raises the panic from Wait.
func (s *sequencer[T]) submit(p *pool.Pool, fn func() T) {
	s.window <- struct{}{}
	seq := s.issued
	s.issued++
	p.Go(func() {
		var v T
		defer func() { s.done(seq, v) }()
		v = fn()
	})
}

func (s *sequencer[T]) done(seq uint64, v T) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if seq != s.next {
		s.pending[seq] = v
		return
	}
	s.release(v)
	for {
		v, ok := s.pending[s.next]
		if !ok {
			return
		}
		delete(s.pending, s.next)
		s.release(v)
	}
}

// release emits v, which carries sequence number s.next, and frees its window
// slot. The receive cannot block: the task holds a token.
func (s *sequencer[T]) release(v T) {
	s.emit(v)
	s.next++
	<-s.window
}
