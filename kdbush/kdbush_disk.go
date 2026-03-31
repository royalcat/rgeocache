package kdbush

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sync"
)

// ---------------------------------------------------------------------------
// Generic constraints for serializable point data
// ---------------------------------------------------------------------------

// binaryData combines both marshal and unmarshal interfaces.
type binaryData interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
}

// binaryPointer constrains VP to be a pointer to V that implements both
// BinaryMarshaler and BinaryUnmarshaler.  This is the standard Go generics
// trick for working with types whose UnmarshalBinary has a pointer receiver.
//
// Usage: [V encoding.BinaryMarshaler, VP binaryPointer[V]]
type binaryPointer[T any] interface {
	*T
	binaryData
}

// ---------------------------------------------------------------------------
// Binary format constants
// ---------------------------------------------------------------------------

var (
	diskMagic     = [4]byte{'K', 'D', 'B', 'H'}
	diskVersion   = uint32(1)
	diskByteOrder = binary.LittleEndian
)

const diskHeaderSize = 32

// Binary layout (little-endian, all offsets are byte positions in the file):
//
//	Header  (32 bytes)
//	  [0  : 4 )  magic     [4]byte   "KDBH"
//	  [4  : 8 )  version   uint32    1
//	  [8  : 16)  nodeSize  int64
//	  [16 : 24)  numPoints int64
//	  [24 : 32)  reserved  [8]byte
//
//	Tree section  (spatial index — accessed during every traversal)
//	  [H        : H + N*8  )  idxs    N × int64    sorted original indices
//	  [H + N*8  : H + N*24 )  coords  N × 2 × float64  sorted (x,y) pairs
//
//	Data section  (point payloads — accessed only for matched points)
//	  [D        : D + (N+1)*8 )  offsets  (N+1) × int64  cumulative byte offsets
//	  [B        : EOF         )  blobs    concatenated MarshalBinary output
//
//	where  H = diskHeaderSize (32)
//	       D = H + N*8 + N*16
//	       B = D + (N+1)*8
//
// The data section stores blobs in ORIGINAL point order (index 0, 1, 2 …)
// so that a single original-index lookup requires exactly two ReadAt calls:
// one for the offset pair, one for the blob.

// ---------------------------------------------------------------------------
// DiskKDBush — generic on-disk spatial index
// ---------------------------------------------------------------------------

// DiskKDBush is an immutable on-disk KD-tree spatial index that also stores
// serialized point data.
//
// Build the index and write it to disk in a single call with [BuildDisk].
// Open an existing file with [OpenDisk].  Queries ([Range], [Within]) traverse
// only the compact tree section (indices + coordinates); point data is read
// and unmarshaled lazily, only for points that actually match the query.
//
// The underlying [io.ReaderAt] must remain valid for the lifetime of the
// DiskKDBush.  For best performance back it with an mmap'd file.
type DiskKDBush[V any, VP binaryPointer[V]] struct {
	r              io.ReaderAt
	nodeSize       int
	numPoints      int
	idxsOffset     int64
	coordsOffset   int64
	dataOffsetsOff int64
	dataBlobsOff   int64
	blobPool       sync.Pool // pool of *bytes.Buffer for readPointData
}

// NumPoints returns the number of indexed points.
func (d *DiskKDBush[V, VP]) NumPoints() int { return d.numPoints }

// NodeSize returns the node size used when the index was built.
func (d *DiskKDBush[V, VP]) NodeSize() int { return d.nodeSize }

// ---------------------------------------------------------------------------
// BuildDisk — build the index in memory and write everything to disk
// ---------------------------------------------------------------------------

// BuildDisk builds a KD-tree spatial index from points, serializes all point
// data via [encoding.BinaryMarshaler], and writes the complete on-disk
// representation to w in a single call.
//
// The index can later be opened with [OpenDisk].
//
// Point data is stored in a separate section after the tree so that queries
// can traverse the spatial structure without touching data bytes.
func BuildDisk[V encoding.BinaryMarshaler, VP binaryPointer[V]](
	points []Point[V], nodeSize int, w io.Writer,
) (int64, error) {
	n := len(points)

	// --- build sorted index arrays (reuses package-level sort) -----------
	idxs := make([]int, n)
	coords := make([]float64, 2*n)
	for i, v := range points {
		idxs[i] = i
		coords[i*2] = v.X
		coords[i*2+1] = v.Y
	}
	if n > 0 {
		sort(idxs, coords, nodeSize, 0, n-1, 0)
	}

	// --- marshal every point's Data in original index order --------------
	blobs := make([][]byte, n)
	offsets := make([]int64, n+1) // cumulative byte offsets; offsets[n] = total
	var cumOffset int64
	for i := range n {
		data, err := points[i].Data.MarshalBinary()
		if err != nil {
			return 0, fmt.Errorf("kdbush: marshal point[%d]: %w", i, err)
		}
		blobs[i] = data
		offsets[i] = cumOffset
		cumOffset += int64(len(data))
	}
	offsets[n] = cumOffset

	// --- write everything ------------------------------------------------
	var written int64

	// header
	var header [diskHeaderSize]byte
	copy(header[0:4], diskMagic[:])
	diskByteOrder.PutUint32(header[4:8], diskVersion)
	diskByteOrder.PutUint64(header[8:16], uint64(nodeSize))
	diskByteOrder.PutUint64(header[16:24], uint64(n))
	nn, err := w.Write(header[:])
	written += int64(nn)
	if err != nil {
		return written, fmt.Errorf("kdbush: writing header: %w", err)
	}

	// sorted indices
	n64, err := diskWriteInts(w, idxs)
	written += n64
	if err != nil {
		return written, fmt.Errorf("kdbush: writing indices: %w", err)
	}

	// sorted coordinates
	n64, err = diskWriteFloat64s(w, coords)
	written += n64
	if err != nil {
		return written, fmt.Errorf("kdbush: writing coords: %w", err)
	}

	// data offset table
	n64, err = diskWriteInt64s(w, offsets)
	written += n64
	if err != nil {
		return written, fmt.Errorf("kdbush: writing data offsets: %w", err)
	}

	// data blobs
	n64, err = diskWriteBlobs(w, blobs)
	written += n64
	if err != nil {
		return written, fmt.Errorf("kdbush: writing data blobs: %w", err)
	}

	return written, nil
}

// ---------------------------------------------------------------------------
// OpenDisk — open an existing on-disk index
// ---------------------------------------------------------------------------

// OpenDisk opens an on-disk KDBush index backed by r.
// The data must have been previously written by [BuildDisk].
// Only the 32-byte header is read; all other data is accessed lazily via
// [io.ReaderAt] during queries.
func OpenDisk[V any, VP binaryPointer[V]](r io.ReaderAt) (*DiskKDBush[V, VP], error) {
	var header [diskHeaderSize]byte
	if _, err := r.ReadAt(header[:], 0); err != nil {
		return nil, fmt.Errorf("kdbush: reading header: %w", err)
	}

	var m [4]byte
	copy(m[:], header[0:4])
	if m != diskMagic {
		return nil, fmt.Errorf("kdbush: invalid magic bytes %q", m[:])
	}

	v := diskByteOrder.Uint32(header[4:8])
	if v != diskVersion {
		return nil, fmt.Errorf("kdbush: unsupported version %d (want %d)", v, diskVersion)
	}

	nodeSize := int(diskByteOrder.Uint64(header[8:16]))
	numPoints := int(diskByteOrder.Uint64(header[16:24]))

	idxsOff := int64(diskHeaderSize)
	coordsOff := idxsOff + int64(numPoints)*8
	dataOffsetsOff := coordsOff + int64(numPoints)*16
	dataBlobsOff := dataOffsetsOff + int64(numPoints+1)*8

	return &DiskKDBush[V, VP]{
		r:              r,
		nodeSize:       nodeSize,
		numPoints:      numPoints,
		idxsOffset:     idxsOff,
		coordsOffset:   coordsOff,
		dataOffsetsOff: dataOffsetsOff,
		dataBlobsOff:   dataBlobsOff,
		blobPool: sync.Pool{
			New: func() any {
				return &bytes.Buffer{}
			},
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Range query
// ---------------------------------------------------------------------------

// Range finds all items within the given bounding box.
//
// The tree section (indices + coordinates) is traversed first without touching
// the data section.  Point data is read and unmarshaled only for the final set
// of matching points.
func (d *DiskKDBush[V, VP]) Range(minX, minY, maxX, maxY float64) ([]Point[V], error) {
	if d.numPoints == 0 {
		return nil, nil
	}

	stack := []int{0, d.numPoints - 1, 0}

	// Collect matches as (origIdx, x, y) — no data reads yet.
	var matchIdxs []int
	var matchCoords []float64 // interleaved x, y

	for len(stack) > 0 {
		axis := stack[len(stack)-1]
		right := stack[len(stack)-2]
		left := stack[len(stack)-3]
		stack = stack[:len(stack)-3]

		if left > right {
			continue
		}

		if right-left <= d.nodeSize {
			idxs, coords, err := d.readLeaf(left, right)
			if err != nil {
				return nil, err
			}
			for i, count := 0, right-left+1; i < count; i++ {
				x := coords[2*i]
				y := coords[2*i+1]
				if x >= minX && x <= maxX && y >= minY && y <= maxY {
					matchIdxs = append(matchIdxs, idxs[i])
					matchCoords = append(matchCoords, x, y)
				}
			}
			continue
		}

		m := floor(float64(left+right) / 2.0)

		x, y, err := d.readCoord(m)
		if err != nil {
			return nil, err
		}

		if x >= minX && x <= maxX && y >= minY && y <= maxY {
			idx, err := d.readIdx(m)
			if err != nil {
				return nil, err
			}
			matchIdxs = append(matchIdxs, idx)
			matchCoords = append(matchCoords, x, y)
		}

		nextAxis := (axis + 1) % 2

		if (axis == 0 && minX <= x) || (axis != 0 && minY <= y) {
			stack = append(stack, left, m-1, nextAxis)
		}
		if (axis == 0 && maxX >= x) || (axis != 0 && maxY >= y) {
			stack = append(stack, m+1, right, nextAxis)
		}
	}

	// Now read + unmarshal data only for matched points.
	result := make([]Point[V], len(matchIdxs))
	for i, origIdx := range matchIdxs {
		data, err := d.readPointData(origIdx)
		if err != nil {
			return nil, err
		}
		result[i] = Point[V]{
			X:    matchCoords[2*i],
			Y:    matchCoords[2*i+1],
			Data: data,
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Within query
// ---------------------------------------------------------------------------

// Within finds all items within the given radius from (qx, qy).
//
// The tree section is traversed using only coordinates.  For each spatially
// matched point, its data is read from disk, unmarshaled, and passed to the
// handler as a complete [Point].  Return false from handler to stop early.
func (d *DiskKDBush[V, VP]) Within(qx, qy, radius float64, handler func(p Point[V]) bool) error {
	if d.numPoints == 0 {
		return nil
	}

	stack := []int{0, d.numPoints - 1, 0}
	r2 := radius * radius

	for len(stack) > 0 {
		axis := stack[len(stack)-1]
		right := stack[len(stack)-2]
		left := stack[len(stack)-3]
		stack = stack[:len(stack)-3]

		if left > right {
			continue
		}

		if right-left <= d.nodeSize {
			idxs, coords, err := d.readLeaf(left, right)
			if err != nil {
				return err
			}
			for i, count := 0, right-left+1; i < count; i++ {
				x := coords[2*i]
				y := coords[2*i+1]
				if sqrtDist(x, y, qx, qy) <= r2 {
					data, err := d.readPointData(idxs[i])
					if err != nil {
						return err
					}
					if !handler(Point[V]{X: x, Y: y, Data: data}) {
						return nil
					}
				}
			}
			continue
		}

		m := floor(float64(left+right) / 2.0)

		x, y, err := d.readCoord(m)
		if err != nil {
			return err
		}

		if sqrtDist(x, y, qx, qy) <= r2 {
			idx, err := d.readIdx(m)
			if err != nil {
				return err
			}
			data, err := d.readPointData(idx)
			if err != nil {
				return err
			}
			if !handler(Point[V]{X: x, Y: y, Data: data}) {
				return nil
			}
		}

		nextAxis := (axis + 1) % 2

		if (axis == 0 && qx-radius <= x) || (axis != 0 && qy-radius <= y) {
			stack = append(stack, left, m-1, nextAxis)
		}
		if (axis == 0 && qx+radius >= x) || (axis != 0 && qy+radius >= y) {
			stack = append(stack, m+1, right, nextAxis)
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Tree-section read helpers  (used during every traversal)
// ---------------------------------------------------------------------------

// readIdx reads a single original-index value at sorted position i.
func (d *DiskKDBush[V, VP]) readIdx(i int) (int, error) {
	var buf [8]byte
	_, err := d.r.ReadAt(buf[:], d.idxsOffset+int64(i)*8)
	if err != nil {
		return 0, fmt.Errorf("kdbush: reading idx[%d]: %w", i, err)
	}
	return int(diskByteOrder.Uint64(buf[:])), nil
}

// readCoord reads the (x, y) coordinate pair for sorted position i.
func (d *DiskKDBush[V, VP]) readCoord(i int) (x, y float64, err error) {
	var buf [16]byte
	_, err = d.r.ReadAt(buf[:], d.coordsOffset+int64(i)*16)
	if err != nil {
		return 0, 0, fmt.Errorf("kdbush: reading coord[%d]: %w", i, err)
	}
	x = math.Float64frombits(diskByteOrder.Uint64(buf[0:8]))
	y = math.Float64frombits(diskByteOrder.Uint64(buf[8:16]))
	return x, y, nil
}

// readLeaf batch-reads indices and coordinates for sorted positions [left, right].
// Two ReadAt calls instead of per-element reads.
func (d *DiskKDBush[V, VP]) readLeaf(left, right int) (idxs []int, coords []float64, err error) {
	count := right - left + 1
	if count <= 0 {
		return nil, nil, nil
	}

	ibuf := make([]byte, count*8)
	if _, err = d.r.ReadAt(ibuf, d.idxsOffset+int64(left)*8); err != nil {
		return nil, nil, fmt.Errorf("kdbush: reading idxs[%d:%d]: %w", left, right, err)
	}
	idxs = make([]int, count)
	for i := range count {
		idxs[i] = int(diskByteOrder.Uint64(ibuf[i*8:]))
	}

	cbuf := make([]byte, count*16)
	if _, err = d.r.ReadAt(cbuf, d.coordsOffset+int64(left)*16); err != nil {
		return nil, nil, fmt.Errorf("kdbush: reading coords[%d:%d]: %w", left, right, err)
	}
	coords = make([]float64, count*2)
	for i := range count * 2 {
		coords[i] = math.Float64frombits(diskByteOrder.Uint64(cbuf[i*8:]))
	}

	return idxs, coords, nil
}

// ---------------------------------------------------------------------------
// Data-section read helper  (called only for matched points)
// ---------------------------------------------------------------------------

// readPointData reads and unmarshals the data payload for the point at the
// given original index.  Exactly two ReadAt calls: one for the offset pair,
// one for the blob.
func (d *DiskKDBush[V, VP]) readPointData(origIdx int) (V, error) {
	// Read offsets[origIdx] and offsets[origIdx+1] in one call.
	var obuf [16]byte
	if _, err := d.r.ReadAt(obuf[:], d.dataOffsetsOff+int64(origIdx)*8); err != nil {
		var zero V
		return zero, fmt.Errorf("kdbush: reading data offset[%d]: %w", origIdx, err)
	}
	blobStart := int64(diskByteOrder.Uint64(obuf[0:8]))
	blobEnd := int64(diskByteOrder.Uint64(obuf[8:16]))
	blobLen := int(blobEnd - blobStart)

	v := new(V)
	if blobLen > 0 {
		buf := d.blobPool.Get().(*bytes.Buffer)
		defer func() {
			buf.Reset()
			d.blobPool.Put(buf)
		}()
		buf.Grow(blobLen)
		b := buf.Next(blobLen)
		if _, err := d.r.ReadAt(b, d.dataBlobsOff+blobStart); err != nil {
			var zero V
			return zero, fmt.Errorf("kdbush: reading data blob[%d]: %w", origIdx, err)
		}
		err := VP(v).UnmarshalBinary(b)

		if err != nil {
			var zero V
			return zero, fmt.Errorf("kdbush: unmarshal point[%d]: %w", origIdx, err)
		}
	} else {
		if err := VP(v).UnmarshalBinary(nil); err != nil {
			var zero V
			return zero, fmt.Errorf("kdbush: unmarshal point[%d]: %w", origIdx, err)
		}
	}

	return *v, nil
}

// ---------------------------------------------------------------------------
// Write helpers  (used by BuildDisk)
// ---------------------------------------------------------------------------

const diskWriteChunkSize = 4096 // elements per write ≈ 32 KiB buffer

func diskWriteInts(w io.Writer, ints []int) (int64, error) {
	var written int64
	if len(ints) == 0 {
		return 0, nil
	}
	chunkLen := min(len(ints), diskWriteChunkSize)
	buf := make([]byte, chunkLen*8)

	for i := 0; i < len(ints); {
		n := min(len(ints)-i, diskWriteChunkSize)
		for j := range n {
			diskByteOrder.PutUint64(buf[j*8:], uint64(ints[i+j]))
		}
		nn, err := w.Write(buf[:n*8])
		written += int64(nn)
		if err != nil {
			return written, err
		}
		i += n
	}
	return written, nil
}

func diskWriteFloat64s(w io.Writer, floats []float64) (int64, error) {
	var written int64
	if len(floats) == 0 {
		return 0, nil
	}
	chunkLen := min(len(floats), diskWriteChunkSize)
	buf := make([]byte, chunkLen*8)

	for i := 0; i < len(floats); {
		n := min(len(floats)-i, diskWriteChunkSize)
		for j := range n {
			diskByteOrder.PutUint64(buf[j*8:], math.Float64bits(floats[i+j]))
		}
		nn, err := w.Write(buf[:n*8])
		written += int64(nn)
		if err != nil {
			return written, err
		}
		i += n
	}
	return written, nil
}

func diskWriteInt64s(w io.Writer, ints []int64) (int64, error) {
	var written int64
	if len(ints) == 0 {
		return 0, nil
	}
	chunkLen := min(len(ints), diskWriteChunkSize)
	buf := make([]byte, chunkLen*8)

	for i := 0; i < len(ints); {
		n := min(len(ints)-i, diskWriteChunkSize)
		for j := range n {
			diskByteOrder.PutUint64(buf[j*8:], uint64(ints[i+j]))
		}
		nn, err := w.Write(buf[:n*8])
		written += int64(nn)
		if err != nil {
			return written, err
		}
		i += n
	}
	return written, nil
}

func diskWriteBlobs(w io.Writer, blobs [][]byte) (int64, error) {
	var written int64
	buf := make([]byte, 0, 64*1024)

	for _, blob := range blobs {
		buf = append(buf, blob...)
		if len(buf) >= 64*1024 {
			nn, err := w.Write(buf)
			written += int64(nn)
			if err != nil {
				return written, err
			}
			buf = buf[:0]
		}
	}
	if len(buf) > 0 {
		nn, err := w.Write(buf)
		written += int64(nn)
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
