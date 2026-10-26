// Package spool stores the cache generation stream on disk so that the
// materialized cache never has to live in RAM.
package spool

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"golang.org/x/exp/mmap"
)

const (
	// PointRecordSize is the fixed on-disk size of a Point record.
	PointRecordSize = 52
	// EdgeRecordSize is the fixed on-disk size of an Edge record.
	EdgeRecordSize = 24

	pointsFile = "points.bin"
	edgesFile  = "edges.bin"
	bufSize    = 1 << 20
)

// Point is one cache point as it is written during parsing. NodeKey is the OSM
// node id for a road shape point, a unique synthetic key for an interpolated
// road point, and 0 for every other point. ClaimSeq is the way's deterministic
// input order; it decides which way's attributes win when several ways share a
// node.
type Point struct {
	X, Y                                    float64
	NameID, StreetID, HouseNumberID, CityID uint32
	RegionID                                uint32
	Weight, GeoType                         uint8
	NodeKey                                 uint64
	ClaimSeq                                uint32
}

// Edge is one road graph edge. From and To are point occurrence indexes; the
// writer canonicalizes them to graph node ids.
type Edge struct {
	From, To         uint32
	StreetID, NameID uint32
	Class, Oneway    uint8
}

// Writer appends points and edges to a spool directory.
type Writer struct {
	dir string
	pf  *os.File
	ef  *os.File
	pw  *bufio.Writer
	ew  *bufio.Writer

	numPoints int
	numEdges  int
}

// Create creates (or truncates) a spool in dir.
func Create(dir string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("spool: creating directory: %w", err)
	}
	pf, err := os.Create(filepath.Join(dir, pointsFile))
	if err != nil {
		return nil, fmt.Errorf("spool: creating points file: %w", err)
	}
	ef, err := os.Create(filepath.Join(dir, edgesFile))
	if err != nil {
		_ = pf.Close()
		return nil, fmt.Errorf("spool: creating edges file: %w", err)
	}
	return &Writer{
		dir: dir,
		pf:  pf,
		ef:  ef,
		pw:  bufio.NewWriterSize(pf, bufSize),
		ew:  bufio.NewWriterSize(ef, bufSize),
	}, nil
}

// Dir returns the directory backing the spool.
func (w *Writer) Dir() string { return w.dir }

// NumPoints returns the number of points written so far.
func (w *Writer) NumPoints() int { return w.numPoints }

// NumEdges returns the number of edges written so far.
func (w *Writer) NumEdges() int { return w.numEdges }

// WritePoint appends p.
func (w *Writer) WritePoint(p Point) error {
	var b [PointRecordSize]byte
	binary.LittleEndian.PutUint64(b[0:8], math.Float64bits(p.X))
	binary.LittleEndian.PutUint64(b[8:16], math.Float64bits(p.Y))
	binary.LittleEndian.PutUint32(b[16:20], p.NameID)
	binary.LittleEndian.PutUint32(b[20:24], p.StreetID)
	binary.LittleEndian.PutUint32(b[24:28], p.HouseNumberID)
	binary.LittleEndian.PutUint32(b[28:32], p.CityID)
	binary.LittleEndian.PutUint32(b[32:36], p.RegionID)
	b[36] = p.Weight
	b[37] = p.GeoType
	binary.LittleEndian.PutUint64(b[38:46], p.NodeKey)
	binary.LittleEndian.PutUint32(b[46:50], p.ClaimSeq)
	if _, err := w.pw.Write(b[:]); err != nil {
		return fmt.Errorf("spool: writing point %d: %w", w.numPoints, err)
	}
	w.numPoints++
	return nil
}

// WriteEdge appends e.
func (w *Writer) WriteEdge(e Edge) error {
	var b [EdgeRecordSize]byte
	binary.LittleEndian.PutUint32(b[0:4], e.From)
	binary.LittleEndian.PutUint32(b[4:8], e.To)
	binary.LittleEndian.PutUint32(b[8:12], e.StreetID)
	binary.LittleEndian.PutUint32(b[12:16], e.NameID)
	b[16] = e.Class
	b[17] = e.Oneway
	if _, err := w.ew.Write(b[:]); err != nil {
		return fmt.Errorf("spool: writing edge %d: %w", w.numEdges, err)
	}
	w.numEdges++
	return nil
}

// Close flushes and closes both files.
func (w *Writer) Close() error {
	err := w.pw.Flush()
	if e := w.ew.Flush(); err == nil {
		err = e
	}
	if e := w.pf.Close(); err == nil {
		err = e
	}
	if e := w.ef.Close(); err == nil {
		err = e
	}
	return err
}

// Reader reads a spool directory.
type Reader struct {
	dir       string
	numPoints int
	numEdges  int
}

// Open opens a spool directory and derives the record counts from the file
// sizes.
func Open(dir string) (*Reader, error) {
	r := &Reader{dir: dir}
	ps, err := os.Stat(filepath.Join(dir, pointsFile))
	if err != nil {
		return nil, fmt.Errorf("spool: opening points file: %w", err)
	}
	es, err := os.Stat(filepath.Join(dir, edgesFile))
	if err != nil {
		return nil, fmt.Errorf("spool: opening edges file: %w", err)
	}
	if ps.Size()%PointRecordSize != 0 {
		return nil, fmt.Errorf("spool: points file size %d is not a multiple of %d", ps.Size(), PointRecordSize)
	}
	if es.Size()%EdgeRecordSize != 0 {
		return nil, fmt.Errorf("spool: edges file size %d is not a multiple of %d", es.Size(), EdgeRecordSize)
	}
	r.numPoints = int(ps.Size() / PointRecordSize)
	r.numEdges = int(es.Size() / EdgeRecordSize)
	return r, nil
}

// Dir returns the spool directory.
func (r *Reader) Dir() string { return r.dir }

// NumPoints returns the number of stored points.
func (r *Reader) NumPoints() int { return r.numPoints }

// NumEdges returns the number of stored edges.
func (r *Reader) NumEdges() int { return r.numEdges }

// IteratePoints calls fn for every point in storage order. The occ argument is
// the point index, which is the point occurrence id used by edges.
func (r *Reader) IteratePoints(fn func(occ int, p Point) error) error {
	f, err := os.Open(filepath.Join(r.dir, pointsFile))
	if err != nil {
		return fmt.Errorf("spool: opening points file: %w", err)
	}
	defer func() { _ = f.Close() }()

	br := bufio.NewReaderSize(f, bufSize)
	var b [PointRecordSize]byte
	for i := 0; i < r.numPoints; i++ {
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return fmt.Errorf("spool: reading point %d: %w", i, err)
		}
		if err := fn(i, decodePoint(b[:])); err != nil {
			return err
		}
	}
	return nil
}

// IterateEdges calls fn for every edge in storage order.
func (r *Reader) IterateEdges(fn func(i int, e Edge) error) error {
	f, err := os.Open(filepath.Join(r.dir, edgesFile))
	if err != nil {
		return fmt.Errorf("spool: opening edges file: %w", err)
	}
	defer func() { _ = f.Close() }()

	br := bufio.NewReaderSize(f, bufSize)
	var b [EdgeRecordSize]byte
	for i := 0; i < r.numEdges; i++ {
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return fmt.Errorf("spool: reading edge %d: %w", i, err)
		}
		if err := fn(i, decodeEdge(b[:])); err != nil {
			return err
		}
	}
	return nil
}

// MmapEdges memory-maps the edges file for random access.
func (r *Reader) MmapEdges() (*mmap.ReaderAt, error) {
	return mmap.Open(filepath.Join(r.dir, edgesFile))
}

// MmapPoints memory-maps the points file for random access.
func (r *Reader) MmapPoints() (*mmap.ReaderAt, error) {
	return mmap.Open(filepath.Join(r.dir, pointsFile))
}

// ReadEdgeAt reads edge i from an mmap'd edges file.
func ReadEdgeAt(ra *mmap.ReaderAt, i int) (Edge, error) {
	var b [EdgeRecordSize]byte
	if _, err := ra.ReadAt(b[:], int64(i)*EdgeRecordSize); err != nil {
		return Edge{}, fmt.Errorf("spool: reading edge %d: %w", i, err)
	}
	return decodeEdge(b[:]), nil
}

// ReadPointAt reads point i from an mmap'd points file.
func ReadPointAt(ra *mmap.ReaderAt, i int) (Point, error) {
	var b [PointRecordSize]byte
	if _, err := ra.ReadAt(b[:], int64(i)*PointRecordSize); err != nil {
		return Point{}, fmt.Errorf("spool: reading point %d: %w", i, err)
	}
	return decodePoint(b[:]), nil
}

func decodePoint(b []byte) Point {
	return Point{
		X:             math.Float64frombits(binary.LittleEndian.Uint64(b[0:8])),
		Y:             math.Float64frombits(binary.LittleEndian.Uint64(b[8:16])),
		NameID:        binary.LittleEndian.Uint32(b[16:20]),
		StreetID:      binary.LittleEndian.Uint32(b[20:24]),
		HouseNumberID: binary.LittleEndian.Uint32(b[24:28]),
		CityID:        binary.LittleEndian.Uint32(b[28:32]),
		RegionID:      binary.LittleEndian.Uint32(b[32:36]),
		Weight:        b[36],
		GeoType:       b[37],
		NodeKey:       binary.LittleEndian.Uint64(b[38:46]),
		ClaimSeq:      binary.LittleEndian.Uint32(b[46:50]),
	}
}

func decodeEdge(b []byte) Edge {
	return Edge{
		From:     binary.LittleEndian.Uint32(b[0:4]),
		To:       binary.LittleEndian.Uint32(b[4:8]),
		StreetID: binary.LittleEndian.Uint32(b[8:12]),
		NameID:   binary.LittleEndian.Uint32(b[12:16]),
		Class:    b[16],
		Oneway:   b[17],
	}
}
