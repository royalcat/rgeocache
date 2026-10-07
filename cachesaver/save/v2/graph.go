package savev2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	savev2proto "github.com/royalcat/rgeocache/cachesaver/save/v2/proto"
	"google.golang.org/protobuf/proto"
)

// Graph section layout ("RGGR"), appended after the point KDBH block:
//
//	"RGGR"           [4]byte
//	version          uint32 (1)
//	flags            uint32 (bit 0: direction metadata present)
//	edge_count       uint64
//	max_half_extent  float64  (degrees; query-expansion bound for the midpoint index)
//	total_size       uint64   (bytes of the whole section, including this header)
//	KDBH block over edge midpoints, payload = graphEdgeRecord (18 bytes):
//	  from_pos u32 | to_pos u32 | street_id u32 | name_id u32 | class u8 | oneway u8
//
// Edge endpoints are point positions in the KDBH sorted order, so a reader
// resolves coordinates with the same index the point index uses. The section is
// invisible to readers that predate it: they stop at the end of the point KDBH
// block.
const (
	graphMagic             = "RGGR"
	graphVersion           = uint32(1)
	graphFlagHasDirections = uint32(1)
	graphHeaderSize        = 36
	graphEdgeRecordSize    = 18
)

// ErrNoGraphSection is returned by [FindGraphSection] when the cache has no
// trailing graph section.
var ErrNoGraphSection = errors.New("savev2: cache has no graph section")

// graphEdgeRecord is the on-disk payload of the graph edge index.
type graphEdgeRecord struct {
	FromPos  uint32
	ToPos    uint32
	StreetID uint32
	NameID   uint32
	Class    uint8
	Oneway   uint8
}

// GraphEdgeRecord is the exported view of an on-disk graph edge.
type GraphEdgeRecord struct {
	FromPos  uint32
	ToPos    uint32
	StreetID uint32
	NameID   uint32
	Class    uint8
	Oneway   uint8
}

// MarshalBinary implements encoding.BinaryMarshaler.
func (e graphEdgeRecord) MarshalBinary() ([]byte, error) {
	buf := make([]byte, graphEdgeRecordSize)
	binary.LittleEndian.PutUint32(buf[0:4], e.FromPos)
	binary.LittleEndian.PutUint32(buf[4:8], e.ToPos)
	binary.LittleEndian.PutUint32(buf[8:12], e.StreetID)
	binary.LittleEndian.PutUint32(buf[12:16], e.NameID)
	buf[16] = e.Class
	buf[17] = e.Oneway
	return buf, nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler.
func (e *graphEdgeRecord) UnmarshalBinary(data []byte) error {
	if len(data) < graphEdgeRecordSize {
		return fmt.Errorf("savev2: invalid graph edge record size: got %d, want %d", len(data), graphEdgeRecordSize)
	}
	e.FromPos = binary.LittleEndian.Uint32(data[0:4])
	e.ToPos = binary.LittleEndian.Uint32(data[4:8])
	e.StreetID = binary.LittleEndian.Uint32(data[8:12])
	e.NameID = binary.LittleEndian.Uint32(data[12:16])
	e.Class = data[16]
	e.Oneway = data[17]
	return nil
}

// graphEdgeIndexSize returns the exact KDBH block size for edgeCount records
// of graphEdgeRecordSize bytes: header + idxs + coords + offsets + blobs.
func graphEdgeIndexSize(edgeCount uint64) int64 {
	return 32 + int64(edgeCount)*8 + int64(edgeCount)*16 + (int64(edgeCount)+1)*8 + int64(edgeCount)*graphEdgeRecordSize
}

// GraphSection describes a parsed trailing graph section of a v2 cache.
type GraphSection struct {
	Offset        int64 // absolute file offset of the section header
	Version       uint32
	Flags         uint32
	EdgeCount     uint64
	MaxHalfExtent float64
	TotalSize     uint64

	dataOffsets int64 // absolute offset of the edge payload offset table
	dataBlobs   int64 // absolute offset of the edge payload blobs
}

// FindGraphSection locates and validates the trailing graph section of a v2
// cache of the given total size. It returns [ErrNoGraphSection] when the cache
// has no graph section.
func FindGraphSection(r io.ReaderAt, size int64) (*GraphSection, error) {
	offset := int64(8) // skip "RGEO" magic + compat level

	var sizeBuf [4]byte
	if _, err := r.ReadAt(sizeBuf[:], offset); err != nil {
		return nil, fmt.Errorf("savev2: graph section: reading header size: %w", err)
	}
	headerSize := int64(binary.LittleEndian.Uint32(sizeBuf[:]))
	offset += 4

	headerBytes := make([]byte, headerSize)
	if _, err := r.ReadAt(headerBytes, offset); err != nil {
		return nil, fmt.Errorf("savev2: graph section: reading header: %w", err)
	}
	var v2header savev2proto.V2Header
	if err := proto.Unmarshal(headerBytes, &v2header); err != nil {
		return nil, fmt.Errorf("savev2: graph section: unmarshalling header: %w", err)
	}
	offset += headerSize + int64(v2header.MetadataSize) + int64(v2header.StringsIndexSize) +
		int64(v2header.StringsDataSize) + int64(v2header.ZonesSize)

	// Point KDBH block: read its header to find the end of the payload section.
	var kdbhHeader [32]byte
	if _, err := r.ReadAt(kdbhHeader[:], offset); err != nil {
		return nil, fmt.Errorf("savev2: graph section: reading point index header: %w", err)
	}
	numPoints := int64(binary.LittleEndian.Uint64(kdbhHeader[16:24]))
	dataOffsets := offset + 32 + numPoints*8 + numPoints*16
	dataBlobs := dataOffsets + (numPoints+1)*8

	var lastOffset [8]byte
	if _, err := r.ReadAt(lastOffset[:], dataOffsets+numPoints*8); err != nil {
		return nil, fmt.Errorf("savev2: graph section: reading point payload size: %w", err)
	}
	graphStart := dataBlobs + int64(binary.LittleEndian.Uint64(lastOffset[:]))

	if size == graphStart {
		return nil, ErrNoGraphSection
	}
	if size < graphStart+graphHeaderSize {
		return nil, fmt.Errorf("savev2: graph section: only %d trailing bytes", size-graphStart)
	}

	var header [graphHeaderSize]byte
	if _, err := r.ReadAt(header[:], graphStart); err != nil {
		return nil, fmt.Errorf("savev2: graph section: reading section header: %w", err)
	}
	if !bytes.Equal(header[0:4], []byte(graphMagic)) {
		return nil, fmt.Errorf("savev2: graph section: invalid magic %q", header[0:4])
	}

	g := &GraphSection{
		Offset:        graphStart,
		Version:       binary.LittleEndian.Uint32(header[4:8]),
		Flags:         binary.LittleEndian.Uint32(header[8:12]),
		EdgeCount:     binary.LittleEndian.Uint64(header[12:20]),
		MaxHalfExtent: math.Float64frombits(binary.LittleEndian.Uint64(header[20:28])),
		TotalSize:     binary.LittleEndian.Uint64(header[28:36]),
	}

	if g.Version != graphVersion {
		return nil, fmt.Errorf("savev2: graph section: unsupported version %d", g.Version)
	}
	if graphStart+int64(g.TotalSize) != size {
		return nil, fmt.Errorf(
			"savev2: graph section: size mismatch: header says %d bytes, file has %d",
			g.TotalSize, size-graphStart,
		)
	}
	expectedIndexSize := graphEdgeIndexSize(g.EdgeCount)
	if int64(g.TotalSize) != int64(graphHeaderSize)+expectedIndexSize {
		return nil, fmt.Errorf(
			"savev2: graph section: index size mismatch for %d edges",
			g.EdgeCount,
		)
	}

	edgeIndexStart := graphStart + graphHeaderSize
	g.dataOffsets = edgeIndexStart + 32 + int64(g.EdgeCount)*8 + int64(g.EdgeCount)*16
	g.dataBlobs = g.dataOffsets + (int64(g.EdgeCount)+1)*8

	return g, nil
}

// Edge returns the graph edge at original index orig.
func (g *GraphSection) Edge(r io.ReaderAt, orig uint64) (GraphEdgeRecord, error) {
	if orig >= g.EdgeCount {
		return GraphEdgeRecord{}, fmt.Errorf("savev2: graph edge index %d out of range (%d edges)", orig, g.EdgeCount)
	}

	var offsetBuf [16]byte
	if _, err := r.ReadAt(offsetBuf[:], g.dataOffsets+int64(orig)*8); err != nil {
		return GraphEdgeRecord{}, fmt.Errorf("savev2: graph edge[%d]: reading offset: %w", orig, err)
	}
	start := int64(binary.LittleEndian.Uint64(offsetBuf[0:8]))
	end := int64(binary.LittleEndian.Uint64(offsetBuf[8:16]))
	if end-start != graphEdgeRecordSize {
		return GraphEdgeRecord{}, fmt.Errorf("savev2: graph edge[%d]: invalid record size %d", orig, end-start)
	}

	var recordBuf [graphEdgeRecordSize]byte
	if _, err := r.ReadAt(recordBuf[:], g.dataBlobs+start); err != nil {
		return GraphEdgeRecord{}, fmt.Errorf("savev2: graph edge[%d]: reading record: %w", orig, err)
	}
	var rec graphEdgeRecord
	if err := rec.UnmarshalBinary(recordBuf[:]); err != nil {
		return GraphEdgeRecord{}, err
	}
	return GraphEdgeRecord(rec), nil
}
