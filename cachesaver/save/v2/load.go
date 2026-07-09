package savev2

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"iter"
	"time"
	"unique"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1proto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	savev2proto "github.com/royalcat/rgeocache/cachesaver/save/v2/proto"
	"github.com/royalcat/rgeocache/kdbush"
	"golang.org/x/exp/mmap"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// Full-memory path: Load from streaming io.Reader
// ---------------------------------------------------------------------------

// Load reads a v2 cache from r and returns lazy iterators for points and zones.
func Load(r io.Reader) (iter.Seq2[cachemodel.Point, error], iter.Seq2[cachemodel.Zone, error], *cachemodel.Metadata, error) {
	var headerSize uint32
	if err := binary.Read(r, binary.LittleEndian, &headerSize); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read header size: %w", err)
	}

	headerBytes := make([]byte, headerSize)
	if _, err := io.ReadFull(r, headerBytes); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read header: %w", err)
	}
	var header savev2proto.V2Header
	if err := proto.Unmarshal(headerBytes, &header); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to unmarshal header: %w", err)
	}

	// Read metadata
	var metadata savev1proto.CacheMetadata
	if err := readProto(r, header.MetadataSize, &metadata); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read metadata: %w", err)
	}

	// Read offset index into memory
	numStrings := header.StringsIndexSize / 4
	stringsIndex := make([]uint32, numStrings)
	if err := binary.Read(r, binary.LittleEndian, &stringsIndex); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read string index: %w", err)
	}

	// Read string data block into memory (needed for the streaming path)
	stringsData := make([]byte, header.StringsDataSize)
	if _, err := io.ReadFull(r, stringsData); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read string data: %w", err)
	}

	// Read and parse zones section
	zonesBytes := make([]byte, header.ZonesSize)
	if _, err := io.ReadFull(r, zonesBytes); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read zones: %w", err)
	}

	parsedZones, err := parseV2Zones(zonesBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to parse zones: %w", err)
	}

	// Read KDBH header
	var kdbhHeader [32]byte
	if _, err := io.ReadFull(r, kdbhHeader[:]); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read KDBH header: %w", err)
	}
	numPoints := int64(binary.LittleEndian.Uint64(kdbhHeader[16:24]))

	// Points iterator: reads V2PointData blobs and resolves strings from the in-memory index.
	pointsIter := func(yield func(cachemodel.Point, error) bool) {
		skipSize := numPoints*8 + numPoints*16
		if _, err := io.CopyN(io.Discard, r, skipSize); err != nil {
			yield(cachemodel.Point{}, fmt.Errorf("v2 load: failed to skip tree: %w", err))
			return
		}

		offsetTable := make([]int64, numPoints+1)
		for i := range offsetTable {
			var buf [8]byte
			if _, err := io.ReadFull(r, buf[:]); err != nil {
				yield(cachemodel.Point{}, fmt.Errorf("v2 load: failed to read offset[%d]: %w", i, err))
				return
			}
			offsetTable[i] = int64(binary.LittleEndian.Uint64(buf[:]))
		}

		for i := range numPoints {
			blobLen := int(offsetTable[i+1] - offsetTable[i])
			var data V2PointData
			if blobLen > 0 {
				blobBuf := make([]byte, blobLen)
				if _, err := io.ReadFull(r, blobBuf); err != nil {
					yield(cachemodel.Point{}, fmt.Errorf("v2 load: failed to read blob[%d]: %w", i, err))
					return
				}
				if err := data.UnmarshalBinary(blobBuf); err != nil {
					yield(cachemodel.Point{}, fmt.Errorf("v2 load: failed to unmarshal blob[%d]: %w", i, err))
					return
				}
			}
			point := resolvePointFromIndex(stringsIndex, stringsData, data)
			if !yield(point, nil) {
				return
			}
		}
	}

	zonesIter := func(yield func(cachemodel.Zone, error) bool) {
		for _, z := range parsedZones {
			if !yield(z, nil) {
				return
			}
		}
	}

	dateCreated, err := time.Parse(time.RFC3339, metadata.DateCreated)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to parse date: %w", err)
	}

	meta := &cachemodel.Metadata{
		Version:     metadata.Version,
		Locale:      metadata.Locale,
		DateCreated: dateCreated,
	}

	return pointsIter, zonesIter, meta, nil
}

// ---------------------------------------------------------------------------
// Low-memory mmap path
// ---------------------------------------------------------------------------

// LoadMmapResult holds the results of loading a v2 cache via mmap.
type LoadMmapResult struct {
	DiskBush          *kdbush.DiskKDBush[V2PointData, *V2PointData]
	StringsIndex      []uint32 // offset index: id → byte offset into string data
	StringsDataOffset int64    // byte offset of the string data block within the mmap'd file
	Zones             []cachemodel.Zone
	Metadata          *cachemodel.Metadata
	mmapReader        *mmap.ReaderAt
}

// Close releases resources held by the result.
func (r *LoadMmapResult) Close() error {
	return r.mmapReader.Close()
}

// LoadMmap opens a v2 cache from a memory-mapped file.
func LoadMmap(reader *mmap.ReaderAt) (*LoadMmapResult, error) {
	offset := int64(8) // skip magic(4) + compat(4)

	// Read V2Header size
	var headerSizeBuf [4]byte
	if _, err := reader.ReadAt(headerSizeBuf[:], offset); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to read header size: %w", err)
	}
	headerSize := binary.LittleEndian.Uint32(headerSizeBuf[:])
	offset += 4

	// Read V2Header
	headerBytes := make([]byte, headerSize)
	if _, err := reader.ReadAt(headerBytes, offset); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to read header: %w", err)
	}
	offset += int64(headerSize)

	var header savev2proto.V2Header
	if err := proto.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to unmarshal header: %w", err)
	}

	// Read metadata
	metadataBytes := make([]byte, header.MetadataSize)
	if _, err := reader.ReadAt(metadataBytes, offset); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to read metadata: %w", err)
	}
	offset += int64(header.MetadataSize)

	var metadata savev1proto.CacheMetadata
	if err := proto.Unmarshal(metadataBytes, &metadata); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to unmarshal metadata: %w", err)
	}

	// Read offset index into memory (small — N×4 bytes)
	numStrings := header.StringsIndexSize / 4
	stringsIndex := make([]uint32, numStrings)
	indexBytes := make([]byte, header.StringsIndexSize)
	if _, err := reader.ReadAt(indexBytes, offset); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to read string index: %w", err)
	}
	for i := range numStrings {
		stringsIndex[i] = binary.LittleEndian.Uint32(indexBytes[i*4 : (i+1)*4])
	}
	offset += int64(header.StringsIndexSize)

	// Record string data block position (don't read it — lazy access)
	stringsDataOffset := offset
	offset += int64(header.StringsDataSize)

	// Read and parse zones section
	zonesBytes := make([]byte, header.ZonesSize)
	if _, err := reader.ReadAt(zonesBytes, offset); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to read zones: %w", err)
	}
	offset += int64(header.ZonesSize)

	parsedZones, err := parseV2Zones(zonesBytes)
	if err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to parse zones: %w", err)
	}

	// Open DiskKDBush at the KDBH block offset
	diskBush, err := kdbush.OpenDisk[V2PointData, *V2PointData](reader, offset)
	if err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to open disk bush: %w", err)
	}

	dateCreated, err := time.Parse(time.RFC3339, metadata.DateCreated)
	if err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to parse date: %w", err)
	}

	return &LoadMmapResult{
		DiskBush:          diskBush,
		StringsIndex:      stringsIndex,
		StringsDataOffset: stringsDataOffset,
		Zones:             parsedZones,
		Metadata: &cachemodel.Metadata{
			Version:     metadata.Version,
			Locale:      metadata.Locale,
			DateCreated: dateCreated,
		},
		mmapReader: reader,
	}, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func readProto(r io.Reader, size uint32, msg proto.Message) error {
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return proto.Unmarshal(buf, msg)
}

// resolvePointFromIndex resolves V2PointData to cachemodel.Point using the string index.
func resolvePointFromIndex(index []uint32, dataBlock []byte, data V2PointData) cachemodel.Point {
	return cachemodel.Point{
		X: 0, Y: 0, // coords not available in streaming path
		Data: cachemodel.Info{
			Name:        unique.Make(readStrByID(index, dataBlock, data.NameID)),
			Street:      unique.Make(readStrByID(index, dataBlock, data.StreetID)),
			HouseNumber: unique.Make(readStrByID(index, dataBlock, data.HouseNumberID)),
			City:        unique.Make(readStrByID(index, dataBlock, data.CityID)),
			Region:      unique.Make(readStrByID(index, dataBlock, data.RegionID)),
			Weight:      data.Weight,
		},
	}
}

// readStrByID reads a null-terminated string from dataBlock using the offset index.
func readStrByID(index []uint32, dataBlock []byte, id uint32) string {
	if id == 0 {
		return ""
	}
	start := index[id]
	if int(start) >= len(dataBlock) {
		return ""
	}
	end := bytes.IndexByte(dataBlock[start:], 0)
	if end < 0 {
		return string(dataBlock[start:])
	}
	return string(dataBlock[start : start+uint32(end)])
}

// parseV2Zones parses the V2ZonesSection protobuf.
func parseV2Zones(data []byte) ([]cachemodel.Zone, error) {
	var section savev2proto.V2ZonesSection
	if err := proto.Unmarshal(data, &section); err != nil {
		return nil, fmt.Errorf("v2: failed to unmarshal zones section: %w", err)
	}

	var zones []cachemodel.Zone
	for _, blob := range section.Blobs {
		var zt cachemodel.ZoneType
		switch blob.ZoneType {
		case 1:
			zt = cachemodel.ZoneRegion
		case 2:
			zt = cachemodel.ZoneCountry
		default:
			continue
		}
		for _, z := range blob.Zones {
			zones = append(zones, cachemodel.Zone{
				Type:    zt,
				Name:    unique.Make(string(z.Name)),
				Bounds:  mapBoundsFromV2(z.Bounds),
				Polygon: mapMultiPolygonFromV2(z.MultiPolygon),
			})
		}
	}
	return zones, nil
}
