package osmpbfdb

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"slices"
	"time"

	_ "github.com/KimMachineGun/automemlimit"

	"github.com/ammario/weakmap"
	"github.com/goware/singleflight"
	"github.com/paulmach/osm"
	"github.com/royalcat/rgeocache/osmpbfdb/osmproto"
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

	cache     weakmap.Map[int64, []osm.Object]
	readGroup singleflight.Group[int64, []osm.Object]

	// id to block offset with it
	// objectIndex   bindex[osm.ObjectID, int64]
	nodeIndex     winindex[osm.NodeID, uint32]
	wayIndex      winindex[osm.WayID, uint32]
	relationIndex winindex[osm.RelationID, uint32]
}

// newDecoder returns a new decoder that reads from r.
func OpenDB(ctx context.Context, r io.ReaderAt) (*DB, error) {

	const maxDuration = time.Duration(^uint64(0) >> 1)

	db := &DB{
		r: r,
	}

	// this is required for weakmap to work properly
	debug.SetGCPercent(80)

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
	bytesRead := int64(0)

	// read OSMHeader
	// NOTE: if the first block is not a header, i.e. after a restart we need
	// to decode that block. It gets pushed on the first "input" below.
	n, blobHeader, blob, err := dec.readFileBlock(0)
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

	// objectIndexBuilder := indexBuilder[osm.ObjectID, int64]{}
	nodeIndexBuilder := indexBuilder[osm.NodeID, uint32]{}
	wayIndexBuilder := indexBuilder[osm.WayID, uint32]{}
	relationIndexBuilder := indexBuilder[osm.RelationID, uint32]{}

	for n, blobHeader, blob, err := dec.readFileBlock(bytesRead); err != io.EOF; n, blobHeader, blob, err = dec.readFileBlock(bytesRead) {
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
			// objectIndexBuilder.Add(obj.ObjectID(), bytesRead)
			switch obj := obj.(type) {
			case *osm.Node:
				nodeIndexBuilder.Add(obj.ID, uint32(bytesRead))
			case *osm.Way:
				wayIndexBuilder.Add(obj.ID, uint32(bytesRead))
			case *osm.Relation:
				relationIndexBuilder.Add(obj.ID, uint32(bytesRead))
			}
		}

		bytesRead += n
	}

	// dec.objectIndex = objectIndexBuilder.Build()
	dec.nodeIndex = nodeIndexBuilder.Build()
	dec.wayIndex = wayIndexBuilder.Build()
	dec.relationIndex = relationIndexBuilder.Build()

	return nil
}

var ErrNotFound = errors.New("object not found")

// func (db *DB) Get(id osm.ObjectID) (osm.Object, error) {
// 	offset, ok := db.objectIndex.Get(id)
// 	if !ok {
// 		return nil, ErrNotFound
// 	}

// 	objects, err := db.readObjects(offset)
// 	if err != nil {
// 		return nil, err
// 	}

// 	for _, obj := range objects {
// 		if obj.ObjectID() == id {
// 			return obj, nil
// 		}
// 	}

// 	return nil, fmt.Errorf("object with id %d not found", id)
// }

func findInObjects[refID ~int64, objType osm.Object](objects []osm.Object, id refID) (objType, error) {
	i, ok := slices.BinarySearchFunc(objects, id, func(o osm.Object, id refID) int {
		return cmp.Compare(o.ObjectID().Ref(), int64(id))
	})

	var obj objType

	if !ok {
		return obj, ErrNotFound
	}

	// used for debugging
	// switch obj := objects[i].(type) {
	// case *osm.Node:
	// 	if obj.ID != osm.NodeID(id) {
	// 		panic("node id mismatch")
	// 	}
	// case *osm.Way:
	// 	if obj.ID != osm.WayID(id) {
	// 		panic("way id mismatch")
	// 	}
	// case *osm.Relation:
	// 	if obj.ID != osm.RelationID(id) {
	// 		panic("relation id mismatch")
	// 	}
	// }

	obj, ok = objects[i].(objType)
	if ok {
		return obj, nil
	}

	return obj, ErrNotFound
}

func (db *DB) GetNode(id osm.NodeID) (*osm.Node, error) {
	offset, ok := db.nodeIndex.Get(id)
	if !ok {
		return nil, ErrNotFound
	}

	objects, err := db.readObjects(int64(offset))
	if err != nil {
		return nil, err
	}

	return findInObjects[osm.NodeID, *osm.Node](objects, id)
}

func (db *DB) GetWay(id osm.WayID) (*osm.Way, error) {
	offset, ok := db.wayIndex.Get(id)
	if !ok {
		return nil, ErrNotFound
	}

	objects, err := db.readObjects(int64(offset))
	if err != nil {
		return nil, err
	}

	return findInObjects[osm.WayID, *osm.Way](objects, id)
}

func (db *DB) GetRelation(id osm.RelationID) (*osm.Relation, error) {
	offset, ok := db.relationIndex.Get(id)
	if !ok {
		return nil, ErrNotFound
	}

	objects, err := db.readObjects(int64(offset))
	if err != nil {
		return nil, err
	}

	return findInObjects[osm.RelationID, *osm.Relation](objects, id)
}

var dataDecoderPool = newSyncPool[*dataDecoder](func() *dataDecoder { return &dataDecoder{} })

func (db *DB) readObjects(offset int64) ([]osm.Object, error) {
	if out, ok := db.cache.Get(offset); ok {
		return out, nil
	}

	out, err, _ := db.readGroup.Do(offset, func() ([]osm.Object, error) {
		return db.cache.Do(offset, func() ([]osm.Object, error) {
			dd := dataDecoderPool.Get()
			defer dataDecoderPool.Put(dd)

			_, _, blob, err := db.readFileBlock(offset)
			if err != nil {
				return nil, err
			}

			objects, err := dd.Decode(blob)
			if err != nil {
				return nil, err
			}

			slices.SortStableFunc(objects, func(a, b osm.Object) int {
				return cmp.Compare(a.ObjectID().Ref(), b.ObjectID().Ref())
			})

			return objects, nil
		})
	})
	return out, err
}

func (dec *DB) readFileBlock(off int64) (int64, *osmproto.BlobHeader, *osmproto.Blob, error) {
	sizeBuf := sizeBufPool.Get()
	defer sizeBufPool.Put(sizeBuf)
	headerBuf := headerBufPool.Get()
	defer headerBufPool.Put(headerBuf)
	blobBuf := blobBufPool.Get()
	defer blobBufPool.Put(blobBuf)

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

func (dec *DB) readBlobHeader(buf []byte, off int64) (*osmproto.BlobHeader, error) {
	n, err := dec.r.ReadAt(buf, off)
	if err != nil {
		return nil, err
	}
	if n != len(buf) {
		return nil, io.ErrUnexpectedEOF
	}

	blobHeader := &osmproto.BlobHeader{}
	if err := proto.Unmarshal(buf, blobHeader); err != nil {
		return nil, err
	}

	if blobHeader.GetDatasize() >= maxBlobSize {
		return nil, errors.New("blob size >= 32Mb")
	}
	return blobHeader, nil
}

func (dec *DB) readBlob(buf []byte, off int64) (*osmproto.Blob, error) {
	n, err := dec.r.ReadAt(buf, off)
	if err != nil {
		return nil, err
	}
	if n != len(buf) {
		return nil, io.ErrUnexpectedEOF
	}

	blob := &osmproto.Blob{}
	if err := proto.Unmarshal(buf, blob); err != nil {
		return nil, err
	}
	return blob, nil
}

func getData(blob *osmproto.Blob, data []byte) ([]byte, error) {
	switch {
	case blob.Raw != nil:
		return blob.GetRaw(), nil

	case blob.ZlibData != nil:
		r, err := zlibReader(blob.GetZlibData())
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

func decodeOSMHeader(blob *osmproto.Blob) (*Header, error) {
	data, err := getData(blob, nil)
	if err != nil {
		return nil, err
	}

	headerBlock := &osmproto.HeaderBlock{}
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
