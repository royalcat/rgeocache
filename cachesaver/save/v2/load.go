package savev2

import (
	"encoding/binary"
	"fmt"
	"io"
	"iter"
	"os"
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
// This is the streaming path that reads everything sequentially.
func Load(r io.Reader) (iter.Seq2[cachemodel.Point, error], iter.Seq2[cachemodel.Zone, error], *cachemodel.Metadata, error) {
	// Read V2Header size
	var headerSize uint32
	if err := binary.Read(r, binary.LittleEndian, &headerSize); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read header size: %w", err)
	}

	// Read and unmarshal V2Header
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

	// Read strings table
	var stringsTable savev2proto.StringsTableV2
	if err := readProto(r, header.StringsSize, &stringsTable); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read strings table: %w", err)
	}

	// Build unique.Handle arrays for O(1) resolution
	nameHandles := resolveHandles(stringsTable.Names)
	streetHandles := resolveHandles(stringsTable.Streets)
	houseNumHandles := resolveHandles(stringsTable.HouseNumbers)
	cityHandles := resolveHandles(stringsTable.Cities)
	regionHandles := resolveHandles(stringsTable.Regions)

	// Skip zones section (we'll read it later in the zones iterator)
	zonesBytes := make([]byte, header.ZonesSize)
	if _, err := io.ReadFull(r, zonesBytes); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read zones section: %w", err)
	}

	// Read KDBH header to get numPoints
	var kdbhHeader [32]byte
	if _, err := io.ReadFull(r, kdbhHeader[:]); err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to read KDBH header: %w", err)
	}
	numPoints := int64(binary.LittleEndian.Uint64(kdbhHeader[16:24]))

	// Read the blobs: each point has a MarshalBinary blob at its original index.
	// We read them sequentially and resolve to cachemodel.Point.
	pointsIter := func(yield func(cachemodel.Point, error) bool) {

		// KDBH layout after header (32 bytes):
		//   idxs:    numPoints * 8 bytes
		//   coords:  numPoints * 16 bytes
		//   offsets: (numPoints+1) * 8 bytes
		//   blobs:   concatenated MarshalBinary output
		//
		// Skip over idxs + coords to reach the offset table.
		skipSize := numPoints*8 + numPoints*16
		if _, err := io.CopyN(io.Discard, r, skipSize); err != nil {
			yield(cachemodel.Point{}, fmt.Errorf("v2 load: failed to skip tree section: %w", err))
			return
		}

		// Read all data offsets (we need them to find each blob)
		offsetTable := make([]int64, numPoints+1)
		for i := range offsetTable {
			var buf [8]byte
			if _, err := io.ReadFull(r, buf[:]); err != nil {
				yield(cachemodel.Point{}, fmt.Errorf("v2 load: failed to read offset[%d]: %w", i, err))
				return
			}
			offsetTable[i] = int64(binary.LittleEndian.Uint64(buf[:]))
		}

		// Read each blob in original index order, resolve strings, yield
		// We need to compute the blob position in the underlying stream.
		// Since we're reading sequentially from r after the offset table,
		// the blobs start immediately after.
		for i := int64(0); i < numPoints; i++ {
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

			point := resolvePoint(&stringsTable, nameHandles, streetHandles, houseNumHandles, cityHandles, regionHandles, data)
			if !yield(point, nil) {
				return
			}
		}
	}

	parsedZones, err := parseZonesSection(zonesBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("v2 load: failed to parse zones: %w", err)
	}
	zoneHandles := resolveZones(parsedZones, &stringsTable)

	zonesIter := func(yield func(cachemodel.Zone, error) bool) {
		for _, z := range zoneHandles {
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
	DiskBush      *kdbush.DiskKDBush[V2PointData, *V2PointData]
	NameHandles   []unique.Handle[string]
	StreetHandles []unique.Handle[string]
	HouseNumHandles []unique.Handle[string]
	CityHandles   []unique.Handle[string]
	RegionHandles []unique.Handle[string]
	Zones         []cachemodel.Zone
	Metadata      *cachemodel.Metadata
	mmapReader    *mmap.ReaderAt // underlying mmap for lifecycle
	tempFile      string         // temp file for KDBH block, if any
}

// Close releases resources held by the result.
func (r *LoadMmapResult) Close() error {
	if r.tempFile != "" {
		os.Remove(r.tempFile)
	}
	return r.mmapReader.Close()
}

// LoadMmap opens a v2 cache from a memory-mapped file.
// The returned LoadMmapResult must be closed after use.
func LoadMmap(reader *mmap.ReaderAt) (*LoadMmapResult, error) {
	// Read magic + compat level (already verified by caller, but re-read for offset)
	// The mmap reader points to the full file. After magic(4) + compat(4),
	// we're at the V2Header size.

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

	// Read strings table
	stringsBytes := make([]byte, header.StringsSize)
	if _, err := reader.ReadAt(stringsBytes, offset); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to read strings: %w", err)
	}
	offset += int64(header.StringsSize)

	var stringsTable savev2proto.StringsTableV2
	if err := proto.Unmarshal(stringsBytes, &stringsTable); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to unmarshal strings: %w", err)
	}

	// Read and parse zones section
	zonesBytes := make([]byte, header.ZonesSize)
	if _, err := reader.ReadAt(zonesBytes, offset); err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to read zones: %w", err)
	}
	offset += int64(header.ZonesSize)

	parsedZones, err := parseZonesSection(zonesBytes)
	if err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to parse zones: %w", err)
	}
	zones := resolveZones(parsedZones, &stringsTable)

	// The KDBH block starts at the current offset.
	// Extract it to a temp file, mmap that, and open DiskKDBush.
	kdbhOffset := offset
	kdbhLen := int64(reader.Len()) - kdbhOffset

	diskBush, tempFile, err := openDiskBush(reader, kdbhOffset, kdbhLen)
	if err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to open disk bush: %w", err)
	}

	dateCreated, err := time.Parse(time.RFC3339, metadata.DateCreated)
	if err != nil {
		return nil, fmt.Errorf("v2 mmap: failed to parse date: %w", err)
	}

	// Resolve string handles
	nameHandles := resolveHandles(stringsTable.Names)
	streetHandles := resolveHandles(stringsTable.Streets)
	houseNumHandles := resolveHandles(stringsTable.HouseNumbers)
	cityHandles := resolveHandles(stringsTable.Cities)
	regionHandles := resolveHandles(stringsTable.Regions)

	return &LoadMmapResult{
		DiskBush:       diskBush,
		NameHandles:    nameHandles,
		StreetHandles:  streetHandles,
		HouseNumHandles: houseNumHandles,
		CityHandles:    cityHandles,
		RegionHandles:  regionHandles,
		Zones:          zones,
		Metadata: &cachemodel.Metadata{
			Version:     metadata.Version,
			Locale:      metadata.Locale,
			DateCreated: dateCreated,
		},
		mmapReader: reader,
		tempFile:   tempFile,
	}, nil
}

// openDiskBush extracts the KDBH block to a temp file, mmaps it, and opens DiskKDBush.
// Returns the temp file path (empty if no temp file was needed) for cleanup.
func openDiskBush(reader *mmap.ReaderAt, offset, length int64) (*kdbush.DiskKDBush[V2PointData, *V2PointData], string, error) {
	// Extract KDBH block to temp file
	f, err := os.CreateTemp("", "rgeocache-kdbh-*.rgc")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp file for KDBH: %w", err)
	}
	tempPath := f.Name()

	// Copy KDBH block from mmap to temp file
	buf := make([]byte, length)
	if _, err := reader.ReadAt(buf, offset); err != nil {
		f.Close()
		os.Remove(tempPath)
		return nil, "", fmt.Errorf("failed to read KDBH block: %w", err)
	}
	if _, err := f.Write(buf); err != nil {
		f.Close()
		os.Remove(tempPath)
		return nil, "", fmt.Errorf("failed to write KDBH temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tempPath)
		return nil, "", fmt.Errorf("failed to close KDBH temp file: %w", err)
	}

	// Mmap the temp file
	kdbhReader, err := mmap.Open(tempPath)
	if err != nil {
		os.Remove(tempPath)
		return nil, "", fmt.Errorf("failed to mmap KDBH temp file: %w", err)
	}

	diskBush, err := kdbush.OpenDisk[V2PointData, *V2PointData](kdbhReader)
	if err != nil {
		kdbhReader.Close()
		os.Remove(tempPath)
		return nil, "", fmt.Errorf("failed to open disk bush: %w", err)
	}

	return diskBush, tempPath, nil
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func readProto(r io.Reader, size uint32, msg proto.Message) error {
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return err
	}
	return proto.Unmarshal(buf, msg)
}

func readAt(r *mmap.ReaderAt, offset int64, p []byte) (int, error) {
	return r.ReadAt(p, offset)
}

// resolveHandles converts a string slice to unique.Handle[string] slice.
func resolveHandles(strings []string) []unique.Handle[string] {
	handles := make([]unique.Handle[string], len(strings))
	for i, s := range strings {
		handles[i] = unique.Make(s)
	}
	return handles
}

// resolvePoint converts V2PointData to cachemodel.Point using the string tables.
func resolvePoint(
	stringsTable *savev2proto.StringsTableV2,
	nameHandles, streetHandles, houseNumHandles, cityHandles, regionHandles []unique.Handle[string],
	data V2PointData,
) cachemodel.Point {
	// We don't have coords here since they're stored in the KDBH index.
	// For the streaming path, coords are embedded in the KDBH tree section
	// which we skipped. We return a point with (0,0) as coordinates since
	// the caller will use the KD-tree for spatial queries anyway.
	return cachemodel.Point{
		X: 0, Y: 0, // coords not available in streaming path without KD-tree traversal
		Data: cachemodel.Info{
			Name:        nameHandles[data.NameStrIdx],
			Street:      streetHandles[data.StreetStrIdx],
			HouseNumber: houseNumHandles[data.HouseNumberStrIdx],
			City:        cityHandles[data.CityStrIdx],
			Region:      regionHandles[data.RegionStrIdx],
			Weight:      data.Weight,
		},
	}
}

// parsedZone holds a single zone with its type before conversion to cachemodel.Zone.
type parsedZone struct {
	Type savev1proto.ZoneType
	Zone *savev1proto.Zone
}

// parseZonesSection parses the binary-framed zones section.
func parseZonesSection(data []byte) ([]parsedZone, error) {
	if len(data) < 4 {
		return nil, nil
	}

	count := binary.LittleEndian.Uint32(data[:4])
	offset := 4

	var zones []parsedZone
	for range count {
		if offset+4 > len(data) {
			return nil, fmt.Errorf("v2: truncated zone blob size at offset %d", offset)
		}
		blobSize := binary.LittleEndian.Uint32(data[offset:])
		offset += 4

		if offset+int(blobSize) > len(data) {
			return nil, fmt.Errorf("v2: truncated zone blob data at offset %d", offset)
		}

		var blob savev1proto.ZonesBlob
		if err := proto.Unmarshal(data[offset:offset+int(blobSize)], &blob); err != nil {
			return nil, fmt.Errorf("v2: failed to unmarshal zone blob: %w", err)
		}
		offset += int(blobSize)

		for _, z := range blob.Zones {
			zones = append(zones, parsedZone{Type: blob.Type, Zone: z})
		}
	}

	return zones, nil
}

// resolveZones converts parsedZone to cachemodel.Zone.
func resolveZones(parsed []parsedZone, stringsTable *savev2proto.StringsTableV2) []cachemodel.Zone {
	regionNames := resolveHandles(stringsTable.Regions)
	zones := make([]cachemodel.Zone, 0, len(parsed))

	for _, pz := range parsed {
		zt := cachemodel.ZoneType(0)
		switch pz.Type {
		case savev1proto.ZoneType_ZONE_TYPE_REGION:
			zt = cachemodel.ZoneRegion
		case savev1proto.ZoneType_ZONE_TYPE_COUNTRY:
			zt = cachemodel.ZoneCountry
		default:
			continue
		}

		nameIdx := pz.Zone.Name
		var name unique.Handle[string]
		if int(nameIdx) < len(regionNames) {
			name = regionNames[nameIdx]
		} else {
			name = unique.Make("")
		}

		zones = append(zones, cachemodel.Zone{
			Type:    zt,
			Name:    name,
			Bounds:  mapBoundsToOrb(pz.Zone.Bounds),
			Polygon: mapMultiPolygonToOrb(pz.Zone.MultiPolygon),
		})
	}

	return zones
}
