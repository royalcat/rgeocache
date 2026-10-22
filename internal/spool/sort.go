package spool

import (
	"bufio"
	"cmp"
	"container/heap"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// DefaultSortPairs is the number of (key, occ) pairs buffered in RAM before a
// sorted run is spilled to disk. 16M pairs is 256 MiB.
const DefaultSortPairs = 16 * 1024 * 1024

// Pair is one (node key, encounter sequence, point occurrence) entry of the
// external sort.
type Pair struct {
	Key uint64
	Seq uint32
	Occ uint32
}

const pairRecordSize = 16

// PairSorter sorts an arbitrarily large stream of pairs in bounded memory.
// Pairs are ordered by key and then by occurrence.
type PairSorter struct {
	dir      string
	maxPairs int
	buf      []Pair
	runs     []string
}

// NewPairSorter creates a sorter that spills sorted runs into dir.
func NewPairSorter(dir string, maxPairs int) (*PairSorter, error) {
	if maxPairs <= 0 {
		maxPairs = DefaultSortPairs
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("spool: creating sort directory: %w", err)
	}
	return &PairSorter{
		dir:      dir,
		maxPairs: maxPairs,
		buf:      make([]Pair, 0, maxPairs),
	}, nil
}

// Add appends a pair, spilling the buffer when it is full.
func (s *PairSorter) Add(key uint64, seq, occ uint32) error {
	s.buf = append(s.buf, Pair{Key: key, Seq: seq, Occ: occ})
	if len(s.buf) >= s.maxPairs {
		return s.flush()
	}
	return nil
}

func (s *PairSorter) flush() error {
	if len(s.buf) == 0 {
		return nil
	}
	slices.SortFunc(s.buf, func(a, b Pair) int {
		if c := cmp.Compare(a.Key, b.Key); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Seq, b.Seq); c != 0 {
			return c
		}
		return cmp.Compare(a.Occ, b.Occ)
	})

	f, err := os.CreateTemp(s.dir, "pairs-*.bin")
	if err != nil {
		return fmt.Errorf("spool: creating sort run: %w", err)
	}
	name := f.Name()
	bw := bufio.NewWriterSize(f, 1<<20)
	var b [pairRecordSize]byte
	for _, p := range s.buf {
		binary.LittleEndian.PutUint64(b[0:8], p.Key)
		binary.LittleEndian.PutUint32(b[8:12], p.Seq)
		binary.LittleEndian.PutUint32(b[12:16], p.Occ)
		if _, err := bw.Write(b[:]); err != nil {
			_ = f.Close()
			_ = os.Remove(name)
			return fmt.Errorf("spool: writing sort run: %w", err)
		}
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return fmt.Errorf("spool: flushing sort run: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("spool: closing sort run: %w", err)
	}
	s.runs = append(s.runs, name)
	s.buf = s.buf[:0]
	return nil
}

// Iterate emits every pair in ascending (key, occ) order. Sorted runs are
// deleted as they are consumed.
func (s *PairSorter) Iterate(fn func(Pair) error) error {
	if err := s.flush(); err != nil {
		return err
	}
	if len(s.runs) == 0 {
		return nil
	}

	readers := make([]*runCursor, 0, len(s.runs))
	for _, name := range s.runs {
		f, err := os.Open(name)
		if err != nil {
			s.closeRuns()
			return fmt.Errorf("spool: opening sort run: %w", err)
		}
		c := &runCursor{file: f, reader: bufio.NewReaderSize(f, 1<<20)}
		ok, err := c.next()
		if err != nil {
			s.closeRuns()
			return err
		}
		if ok {
			readers = append(readers, c)
		} else {
			_ = f.Close()
		}
	}

	h := runHeap(readers)
	heap.Init(&h)
	for h.Len() > 0 {
		c := h[0]
		p := c.cur
		if err := fn(p); err != nil {
			s.closeRuns()
			return err
		}
		ok, err := c.next()
		if err != nil {
			s.closeRuns()
			return err
		}
		if ok {
			heap.Fix(&h, 0)
		} else {
			_ = c.file.Close()
			heap.Pop(&h)
		}
	}

	s.closeRuns()
	return nil
}

// Close removes any spilled runs and releases the buffer.
func (s *PairSorter) Close() error {
	s.closeRuns()
	s.buf = nil
	return nil
}

func (s *PairSorter) closeRuns() {
	for _, name := range s.runs {
		_ = os.Remove(name)
	}
	s.runs = nil
}

type runCursor struct {
	file   *os.File
	reader *bufio.Reader
	cur    Pair
}

func (c *runCursor) next() (bool, error) {
	var b [pairRecordSize]byte
	if _, err := io.ReadFull(c.reader, b[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return false, nil
		}
		return false, fmt.Errorf("spool: reading sort run: %w", err)
	}
	c.cur = Pair{
		Key: binary.LittleEndian.Uint64(b[0:8]),
		Seq: binary.LittleEndian.Uint32(b[8:12]),
		Occ: binary.LittleEndian.Uint32(b[12:16]),
	}
	return true, nil
}

type runHeap []*runCursor

func (h runHeap) Len() int { return len(h) }
func (h runHeap) Less(i, j int) bool {
	if h[i].cur.Key != h[j].cur.Key {
		return h[i].cur.Key < h[j].cur.Key
	}
	if h[i].cur.Seq != h[j].cur.Seq {
		return h[i].cur.Seq < h[j].cur.Seq
	}
	return h[i].cur.Occ < h[j].cur.Occ
}
func (h runHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *runHeap) Push(x any)   { *h = append(*h, x.(*runCursor)) }
func (h *runHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// SortDir returns the directory used for a spool's temporary sort runs.
func SortDir(spoolDir string) string { return filepath.Join(spoolDir, "sort") }
