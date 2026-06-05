package savev2

import (
	"encoding/binary"
	"io"
	"iter"
	"time"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1proto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	savev2proto "github.com/royalcat/rgeocache/cachesaver/save/v2/proto"
	"github.com/royalcat/rgeocache/kdbush"
	"google.golang.org/protobuf/proto"
)

const defaultNodeSize = 64

// Save writes a v2 cache to w.
//
// File layout:
//
//	[0..3]       "RGEO" magic
//	[4..7]       uint32 compat level = 2
//	[8..11]      uint32 v2header_size
//	[12..H]      V2Header protobuf
//	[H+..]       CacheMetadata protobuf
//	[..+M]       StringsTableV2 protobuf
//	[..+S]       ZonesSection protobuf
//	[..+Z]       KDBH binary block (standard DiskKDBush format)
func Save(w io.Writer, points iter.Seq[cachemodel.Point], zones iter.Seq[cachemodel.Zone], meta cachemodel.Metadata) error {
	dedup := newStringsDedup()

	// Phase 1: Materialize points, build dedup maps
	var v2points []kdbush.Point[V2PointData]
	for p := range points {
		v2points = append(v2points, kdbush.Point[V2PointData]{
			X: p.X,
			Y: p.Y,
			Data: V2PointData{
				NameStrIdx:        uint32(dedup.names.Add(p.Data.Name.Value())),
				StreetStrIdx:      uint32(dedup.streets.Add(p.Data.Street.Value())),
				HouseNumberStrIdx: uint32(dedup.houseNumbers.Add(p.Data.HouseNumber.Value())),
				CityStrIdx:        uint32(dedup.cities.Add(p.Data.City.Value())),
				RegionStrIdx:      uint32(dedup.regions.Add(p.Data.Region.Value())),
				Weight:            p.Data.Weight,
			},
		})
	}

	// Phase 2: Materialize zones
	var regionProtos []*savev1proto.Zone
	var countryProtos []*savev1proto.Zone
	for z := range zones {
		nameIdx := uint32(dedup.regions.Add(z.Name.Value()))
		protoZone := &savev1proto.Zone{
			Name:         nameIdx,
			Bounds:       mapBoundsFromOrb(z.Bounds),
			MultiPolygon: mapMultiPolygonFromOrb(z.Polygon),
		}
		switch z.Type {
		case cachemodel.ZoneRegion:
			regionProtos = append(regionProtos, protoZone)
		case cachemodel.ZoneCountry:
			countryProtos = append(countryProtos, protoZone)
		}
	}

	// Phase 3: Marshal supporting sections
	stringsTable := &savev2proto.StringsTableV2{
		Names:        dedup.names.Slice(),
		Streets:      dedup.streets.Slice(),
		HouseNumbers: dedup.houseNumbers.Slice(),
		Cities:       dedup.cities.Slice(),
		Regions:      dedup.regions.Slice(),
	}
	stringsBytes, err := proto.Marshal(stringsTable)
	if err != nil {
		return err
	}

	metadataProto := &savev1proto.CacheMetadata{
		Version:     meta.Version,
		DateCreated: meta.DateCreated.Format(time.RFC3339),
		Locale:      meta.Locale,
	}
	metadataBytes, err := proto.Marshal(metadataProto)
	if err != nil {
		return err
	}

	// Zones section: binary-framed ZonesBlob messages.
	// Format: [uint32 count] ([uint32 proto_size][proto_bytes])*
	zonesBytes, err := marshalZonesSection(regionProtos, countryProtos)
	if err != nil {
		return err
	}

	header := &savev2proto.V2Header{
		MetadataSize: uint32(len(metadataBytes)),
		StringsSize:  uint32(len(stringsBytes)),
		ZonesSize:    uint32(len(zonesBytes)),
	}
	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return err
	}

	// Phase 4: Write everything sequentially
	// Magic bytes + compat level (written by caller in cachesaver.SaveV2)

	// V2Header size + V2Header
	if err := binary.Write(w, binary.LittleEndian, uint32(len(headerBytes))); err != nil {
		return err
	}
	if _, err := w.Write(headerBytes); err != nil {
		return err
	}

	// CacheMetadata
	if _, err := w.Write(metadataBytes); err != nil {
		return err
	}

	// StringsTableV2
	if _, err := w.Write(stringsBytes); err != nil {
		return err
	}

	// ZonesSection (binary-framed)
	if _, err := w.Write(zonesBytes); err != nil {
		return err
	}

	// KDBH block
	if _, err := kdbush.BuildDisk[V2PointData, *V2PointData](v2points, defaultNodeSize, w); err != nil {
		return err
	}

	return nil
}

// marshalZonesSection packs regions and countries into a binary-framed sequence
// of ZonesBlob messages. Each blob preserves its type via the ZoneType field.
//
// Binary format:
//
//	[uint32 count]  number of zone blobs
//	for each blob:
//	  [uint32 proto_size]
//	  [proto_bytes]  protobuf-encoded ZonesBlob
func marshalZonesSection(regions, countries []*savev1proto.Zone) ([]byte, error) {
	const blobChunkSize = 100

	var blobs [][]byte

	writeChunks := func(zones []*savev1proto.Zone, zt savev1proto.ZoneType) error {
		for i := 0; i < len(zones); i += blobChunkSize {
			end := min(i+blobChunkSize, len(zones))
			blob := &savev1proto.ZonesBlob{
				Type:  zt,
				Zones: zones[i:end],
			}
			blobBytes, err := proto.Marshal(blob)
			if err != nil {
				return err
			}
			blobs = append(blobs, blobBytes)
		}
		return nil
	}

	if err := writeChunks(regions, savev1proto.ZoneType_ZONE_TYPE_REGION); err != nil {
		return nil, err
	}
	if err := writeChunks(countries, savev1proto.ZoneType_ZONE_TYPE_COUNTRY); err != nil {
		return nil, err
	}

	// Calculate total size
	totalSize := 4 // count uint32
	for _, b := range blobs {
		totalSize += 4 + len(b) // size uint32 + proto bytes
	}

	buf := make([]byte, totalSize)
	offset := 0

	binary.LittleEndian.PutUint32(buf[offset:], uint32(len(blobs)))
	offset += 4

	for _, b := range blobs {
		binary.LittleEndian.PutUint32(buf[offset:], uint32(len(b)))
		offset += 4
		copy(buf[offset:], b)
		offset += len(b)
	}

	return buf, nil
}
