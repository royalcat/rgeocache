package osmpbfdb

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/paulmach/osm"
	"github.com/royalcat/rgeocache/kv/osmpbfdb/osmpbf"
	"google.golang.org/protobuf/proto"
)

const (
	sizeBufSize       = 4
	maxBlobHeaderSize = 64 * 1024
	maxBlobSize       = 32 * 1024 * 1024
)

var (
	parseCapabilities = map[string]bool{
		"OsmSchema-V0.6":        true,
		"DenseNodes":            true,
		"HistoricalInformation": true,
	}
)

// osm block data types
const (
	osmHeaderType = "OSMHeader"
	osmDataType   = "OSMData"
)

// Header contains the contents of the header in the pbf file.
type Header struct {
	Bounds               *osm.Bounds
	RequiredFeatures     []string
	OptionalFeatures     []string
	WritingProgram       string
	Source               string
	ReplicationTimestamp time.Time
	ReplicationSeqNum    uint64
	ReplicationBaseURL   string
}

// A Decoder reads and decodes OpenStreetMap PBF data from an input stream.
type DB struct {
	header *Header
	dd     *dataDecoder
	r      io.ReaderAt

	index map[osm.ObjectID]int64 // object id to block offset with it
}

// newDecoder returns a new decoder that reads from r.
func InitDB(ctx context.Context, r io.ReaderAt) (*DB, error) {
	db := &DB{
		r:     r,
		index: make(map[osm.ObjectID]int64),
	}
	err := db.buildIndex()
	if err != nil {
		return nil, err
	}
	return db, nil
}

func (dec *DB) Close() error {
	return nil
}

// buildIndex decoding process using n goroutines.
func (dec *DB) buildIndex() error {

	sizeBuf := make([]byte, 4)
	headerBuf := make([]byte, maxBlobHeaderSize)
	blobBuf := make([]byte, maxBlobSize)

	bytesRead := int64(0)

	// read OSMHeader
	// NOTE: if the first block is not a header, i.e. after a restart we need
	// to decode that block. It gets pushed on the first "input" below.
	n, blobHeader, blob, err := dec.readFileBlock(sizeBuf, headerBuf, blobBuf, 0)
	if err != nil {
		return err
	}
	bytesRead += n

	if blobHeader.GetType() == osmHeaderType {
		var err error
		dec.header, err = decodeOSMHeader(blob)
		if err != nil {
			return err
		}
	}

	dd := &dataDecoder{}

	for n, blobHeader, blob, err := dec.readFileBlock(sizeBuf, headerBuf, blobBuf, bytesRead); err != io.EOF; n, blobHeader, blob, err = dec.readFileBlock(sizeBuf, headerBuf, blobBuf, bytesRead) {
		if err != nil {
			return err
		}

		if blobHeader.GetType() != osmDataType {
			return fmt.Errorf("unexpected fileblock of type %s", blobHeader.GetType())
		}

		objects, err := dd.Decode(blob)
		if err != nil {
			return err
		}

		for _, obj := range objects {
			dec.index[obj.ObjectID()] = bytesRead
		}

		bytesRead += n
	}

	return nil
}

var ErrNotFound = errors.New("object not found")

func (db *DB) Get(id osm.ObjectID) (osm.Object, error) {
	offset, ok := db.index[id]
	if !ok {
		return nil, ErrNotFound
	}

	objects, err := db.readObjects(offset)
	if err != nil {
		return nil, err
	}

	for _, obj := range objects {
		if obj.ObjectID() == id {
			return obj, nil
		}
	}

	return nil, fmt.Errorf("object with id %d not found", id)
}

func (db *DB) readObjects(offset int64) ([]osm.Object, error) {
	sizeBuf := make([]byte, 4)
	headerBuf := make([]byte, maxBlobHeaderSize)
	blobBuf := make([]byte, maxBlobSize)
	dd := &dataDecoder{}

	_, _, blob, err := db.readFileBlock(sizeBuf, headerBuf, blobBuf, offset)
	if err != nil {
		return nil, err
	}

	objects, err := dd.Decode(blob)
	if err != nil {
		return nil, err
	}

	return objects, nil
}

func (dec *DB) readFileBlock(sizeBuf, headerBuf, blobBuf []byte, off int64) (int64, *osmpbf.BlobHeader, *osmpbf.Blob, error) {
	blobHeaderSize, err := dec.readBlobHeaderSize(sizeBuf, off)
	if err != nil {
		return 0, nil, nil, err
	}

	headerBuf = headerBuf[:blobHeaderSize]
	blobHeader, err := dec.readBlobHeader(headerBuf, off+sizeBufSize)
	if err != nil {
		return 0, nil, nil, err
	}

	blobBuf = blobBuf[:blobHeader.GetDatasize()]
	blob, err := dec.readBlob(blobBuf, off+sizeBufSize+int64(blobHeaderSize))
	if err != nil {
		return 0, nil, nil, err
	}

	bytesRead := sizeBufSize + int64(blobHeaderSize) + int64(blobHeader.GetDatasize())

	return bytesRead, blobHeader, blob, nil
}

func (dec *DB) readBlobHeaderSize(buf []byte, off int64) (uint32, error) {
	n, err := dec.r.ReadAt(buf, off)
	if err != nil {
		return 0, err
	}
	if n != len(buf) {
		return 0, io.ErrUnexpectedEOF
	}

	size := binary.BigEndian.Uint32(buf)
	if size >= maxBlobHeaderSize {
		return 0, errors.New("blobHeader size >= 64Kb")
	}
	return size, nil
}

func (dec *DB) readBlobHeader(buf []byte, off int64) (*osmpbf.BlobHeader, error) {
	n, err := dec.r.ReadAt(buf, off)
	if err != nil {
		return nil, err
	}
	if n != len(buf) {
		return nil, io.ErrUnexpectedEOF
	}

	blobHeader := &osmpbf.BlobHeader{}
	if err := proto.Unmarshal(buf, blobHeader); err != nil {
		return nil, err
	}

	if blobHeader.GetDatasize() >= maxBlobSize {
		return nil, errors.New("blob size >= 32Mb")
	}
	return blobHeader, nil
}

func (dec *DB) readBlob(buf []byte, off int64) (*osmpbf.Blob, error) {
	n, err := dec.r.ReadAt(buf, off)
	if err != nil {
		return nil, err
	}
	if n != len(buf) {
		return nil, io.ErrUnexpectedEOF
	}

	blob := &osmpbf.Blob{}
	if err := proto.Unmarshal(buf, blob); err != nil {
		return nil, err
	}
	return blob, nil
}

func getData(blob *osmpbf.Blob, data []byte) ([]byte, error) {
	switch {
	case blob.Raw != nil:
		return blob.GetRaw(), nil

	case blob.ZlibData != nil:
		r, err := zlib.NewReader(bytes.NewReader(blob.GetZlibData()))
		if err != nil {
			return nil, err
		}

		// using the bytes.Buffer allows for the preallocation of the necessary space.
		l := blob.GetRawSize() + bytes.MinRead
		if cap(data) < int(l) {
			data = make([]byte, 0, l+l/10)
		} else {
			data = data[:0]
		}
		buf := bytes.NewBuffer(data)
		if _, err = buf.ReadFrom(r); err != nil {
			return nil, err
		}

		if buf.Len() != int(blob.GetRawSize()) {
			return nil, fmt.Errorf("raw blob data size %d but expected %d", buf.Len(), blob.GetRawSize())
		}

		return buf.Bytes(), nil
	default:
		return nil, errors.New("unknown blob data")
	}
}

func decodeOSMHeader(blob *osmpbf.Blob) (*Header, error) {
	data, err := getData(blob, nil)
	if err != nil {
		return nil, err
	}

	headerBlock := &osmpbf.HeaderBlock{}
	if err := proto.Unmarshal(data, headerBlock); err != nil {
		return nil, err
	}

	// Check we have the parse capabilities
	requiredFeatures := headerBlock.GetRequiredFeatures()
	for _, feature := range requiredFeatures {
		if !parseCapabilities[feature] {
			return nil, fmt.Errorf("parser does not have %s capability", feature)
		}
	}

	// read the header
	header := &Header{
		RequiredFeatures:   headerBlock.GetRequiredFeatures(),
		OptionalFeatures:   headerBlock.GetOptionalFeatures(),
		WritingProgram:     headerBlock.GetWritingprogram(),
		Source:             headerBlock.GetSource(),
		ReplicationBaseURL: headerBlock.GetOsmosisReplicationBaseUrl(),
		ReplicationSeqNum:  uint64(headerBlock.GetOsmosisReplicationSequenceNumber()),
	}

	// convert timestamp epoch seconds to golang time structure if it exists
	if headerBlock.OsmosisReplicationTimestamp != nil {
		header.ReplicationTimestamp = time.Unix(*headerBlock.OsmosisReplicationTimestamp, 0).UTC()
	}
	// read bounding box if it exists
	if headerBlock.Bbox != nil {
		// Units are always in nanodegree and do not obey granularity rules. See osmformat.proto
		header.Bounds = &osm.Bounds{
			MinLon: 1e-9 * float64(*headerBlock.Bbox.Left),
			MaxLon: 1e-9 * float64(*headerBlock.Bbox.Right),
			MinLat: 1e-9 * float64(*headerBlock.Bbox.Bottom),
			MaxLat: 1e-9 * float64(*headerBlock.Bbox.Top),
		}
	}

	return header, nil
}
