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
//	[..+I]       offset index: []uint32 (N unique strings × 4)
//	[..+D]       string data block (null-terminated concatenation)
//	[..+S]       ZonesSection protobuf (V2ZonesSection)
//	[..+Z]       KDBH binary block
func Save(w io.Writer, points iter.Seq[cachemodel.Point], zones iter.Seq[cachemodel.Zone], meta cachemodel.Metadata) error {
	dedup := newStringsDedup()

	// Phase 1: Materialize points with placeholder data.
	// Register strings to get IDs; we'll fill V2PointData after building the index.
	type rawPoint struct {
		x, y float64
		name, street, houseNumber, city, region string
		weight uint8
	}
	var rawPoints []rawPoint
	for p := range points {
		rawPoints = append(rawPoints, rawPoint{
			x: p.X, y: p.Y,
			name:        p.Data.Name.Value(),
			street:      p.Data.Street.Value(),
			houseNumber: p.Data.HouseNumber.Value(),
			city:        p.Data.City.Value(),
			region:      p.Data.Region.Value(),
			weight:      p.Data.Weight,
		})
		// Register strings to reserve IDs
		dedup.names.Add(p.Data.Name.Value())
		dedup.streets.Add(p.Data.Street.Value())
		dedup.houseNumbers.Add(p.Data.HouseNumber.Value())
		dedup.cities.Add(p.Data.City.Value())
		dedup.regions.Add(p.Data.Region.Value())
	}

	// Phase 2: Build offset index and null-terminated string data block
	offsetIndex, stringData := buildStringIndex(dedup)

	// Phase 3: Fill V2PointData using the assigned IDs
	v2points := make([]kdbush.Point[V2PointData], len(rawPoints))
	for i, rp := range rawPoints {
		v2points[i] = kdbush.Point[V2PointData]{
			X: rp.x, Y: rp.y,
			Data: V2PointData{
				NameID:        dedup.names.Add(rp.name),
				StreetID:      dedup.streets.Add(rp.street),
				HouseNumberID: dedup.houseNumbers.Add(rp.houseNumber),
				CityID:        dedup.cities.Add(rp.city),
				RegionID:      dedup.regions.Add(rp.region),
				Weight:        rp.weight,
			},
		}
	}
	rawPoints = nil // release to GC

	// Phase 4: Materialize zones with inline names
	zonesSection := buildZonesSection(zones)
	zonesBytes, err := proto.Marshal(zonesSection)
	if err != nil {
		return err
	}

	// Phase 5: Marshal metadata
	metadataProto := &savev1proto.CacheMetadata{
		Version:     meta.Version,
		DateCreated: meta.DateCreated.Format(time.RFC3339),
		Locale:      meta.Locale,
	}
	metadataBytes, err := proto.Marshal(metadataProto)
	if err != nil {
		return err
	}

	// Phase 6: V2Header
	header := &savev2proto.V2Header{
		MetadataSize:     uint32(len(metadataBytes)),
		StringsIndexSize: uint32(len(offsetIndex) * 4),
		StringsDataSize:  uint32(len(stringData)),
		ZonesSize:        uint32(len(zonesBytes)),
	}
	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return err
	}

	// Phase 7: Write everything sequentially
	if err := binary.Write(w, binary.LittleEndian, uint32(len(headerBytes))); err != nil {
		return err
	}
	if _, err := w.Write(headerBytes); err != nil {
		return err
	}
	if _, err := w.Write(metadataBytes); err != nil {
		return err
	}
	// Write offset index as raw uint32 array
	if err := binary.Write(w, binary.LittleEndian, offsetIndex); err != nil {
		return err
	}
	if _, err := w.Write(stringData); err != nil {
		return err
	}
	if _, err := w.Write(zonesBytes); err != nil {
		return err
	}

	// KDBH block
	if _, err := kdbush.BuildDisk[V2PointData, *V2PointData](v2points, defaultNodeSize, w); err != nil {
		return err
	}

	return nil
}

// buildZonesSection converts zones to V2ZonesSection proto with inline names and geometry.
func buildZonesSection(zones iter.Seq[cachemodel.Zone]) *savev2proto.V2ZonesSection {
	var regionZones []*savev2proto.V2Zone
	var countryZones []*savev2proto.V2Zone

	for z := range zones {
		v2z := &savev2proto.V2Zone{
			Name:         []byte(z.Name.Value()),
			Bounds:       mapBoundsToV2(z.Bounds),
			MultiPolygon: mapMultiPolygonToV2(z.Polygon),
		}
		switch z.Type {
		case cachemodel.ZoneRegion:
			regionZones = append(regionZones, v2z)
		case cachemodel.ZoneCountry:
			countryZones = append(countryZones, v2z)
		}
	}

	sec := &savev2proto.V2ZonesSection{}
	if len(regionZones) > 0 {
		sec.Blobs = append(sec.Blobs, &savev2proto.V2ZoneBlob{
			ZoneType: 1,
			Zones:    regionZones,
		})
	}
	if len(countryZones) > 0 {
		sec.Blobs = append(sec.Blobs, &savev2proto.V2ZoneBlob{
			ZoneType: 2,
			Zones:    countryZones,
		})
	}
	return sec
}
