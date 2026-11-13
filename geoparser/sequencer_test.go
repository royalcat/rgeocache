package geoparser

import (
	"math/rand"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sourcegraph/conc/pool"
)

func TestSequencerEmitsInSubmissionOrder(t *testing.T) {
	const n = 2000

	var got []int
	seq := newSequencer(32, func(v int) { got = append(got, v) })

	p := pool.New().WithMaxGoroutines(8)
	rng := rand.New(rand.NewSource(1))
	delays := make([]time.Duration, n)
	for i := range delays {
		delays[i] = time.Duration(rng.Intn(200)) * time.Microsecond
	}
	for i := 0; i < n; i++ {
		seq.submit(p, func() int {
			time.Sleep(delays[i])
			return i
		})
	}
	p.Wait()

	if len(got) != n {
		t.Fatalf("emitted %d results, want %d", len(got), n)
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("result %d emitted at position %d", v, i)
		}
	}
}

func TestSequencerWindowBoundsOutstandingResults(t *testing.T) {
	const window = 4

	// The first task blocks until released; every later task completes at once.
	// The window must stop the submitter from running more than `window` tasks
	// ahead of the stuck one.
	release := make(chan struct{})
	var started atomic.Int64
	var emitted atomic.Int64
	seq := newSequencer(window, func(int) { emitted.Add(1) })

	p := pool.New().WithMaxGoroutines(16)
	submitted := make(chan struct{})
	go func() {
		defer close(submitted)
		for i := 0; i < 100; i++ {
			seq.submit(p, func() int {
				started.Add(1)
				if i == 0 {
					<-release
				}
				return i
			})
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for started.Load() < window && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let a violation show up

	if got := started.Load(); got > window {
		t.Fatalf("%d tasks started while the first was stuck, window is %d", got, window)
	}
	if got := emitted.Load(); got != 0 {
		t.Fatalf("emitted %d results before the first task finished", got)
	}

	close(release)
	<-submitted
	p.Wait()
	if got := emitted.Load(); got != 100 {
		t.Fatalf("emitted %d results, want 100", got)
	}
}

func TestSequencerEmptyResultsKeepOrder(t *testing.T) {
	var got []int
	seq := newSequencer(8, func(v []int) { got = append(got, v...) })

	p := pool.New().WithMaxGoroutines(4)
	for i := 0; i < 100; i++ {
		seq.submit(p, func() []int {
			if i%3 == 0 {
				return nil
			}
			return []int{i}
		})
	}
	p.Wait()

	prev := 0
	for _, v := range got {
		if v <= prev {
			t.Fatalf("out of order: %d after %d", v, prev)
		}
		prev = v
	}
}
