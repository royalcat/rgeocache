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
//	[..+M]       raw string blob (concatenated unique strings)
//	[..+S]       ZonesSection protobuf (V2ZonesSection)
//	[..+Z]       KDBH binary block
func Save(w io.Writer, points iter.Seq[cachemodel.Point], zones iter.Seq[cachemodel.Zone], meta cachemodel.Metadata) error {
	dedup := newStringsDedup()

	// Phase 1: Materialize points with placeholder data, collect zone info.
	// We can't fill V2PointData yet because string offsets aren't known until
	// the string blob is built.
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
		// Register strings in dedup maps to reserve offsets
		dedup.names.Add(p.Data.Name.Value())
		dedup.streets.Add(p.Data.Street.Value())
		dedup.houseNumbers.Add(p.Data.HouseNumber.Value())
		dedup.cities.Add(p.Data.City.Value())
		dedup.regions.Add(p.Data.Region.Value())
	}

	// Phase 2: Build string blob and finalize V2PointData
	stringsBlob := buildStringsBlob(dedup)

	v2points := make([]kdbush.Point[V2PointData], len(rawPoints))
	for i, rp := range rawPoints {
		v2points[i] = kdbush.Point[V2PointData]{
			X: rp.x, Y: rp.y,
			Data: V2PointData{
				NameOffset:        dedup.names.Add(rp.name).offset,
				NameLen:           dedup.names.Add(rp.name).length,
				StreetOffset:      dedup.streets.Add(rp.street).offset,
				StreetLen:         dedup.streets.Add(rp.street).length,
				HouseNumberOffset: dedup.houseNumbers.Add(rp.houseNumber).offset,
				HouseNumberLen:    dedup.houseNumbers.Add(rp.houseNumber).length,
				CityOffset:        dedup.cities.Add(rp.city).offset,
				CityLen:           dedup.cities.Add(rp.city).length,
				RegionOffset:      dedup.regions.Add(rp.region).offset,
				RegionLen:         dedup.regions.Add(rp.region).length,
				Weight:            rp.weight,
			},
		}
	}
	rawPoints = nil // release to GC

	// Phase 3: Materialize zones with inline names
	zonesSection := buildZonesSection(zones)
	zonesBytes, err := proto.Marshal(zonesSection)
	if err != nil {
		return err
	}

	// Phase 4: Marshal metadata
	metadataProto := &savev1proto.CacheMetadata{
		Version:     meta.Version,
		DateCreated: meta.DateCreated.Format(time.RFC3339),
		Locale:      meta.Locale,
	}
	metadataBytes, err := proto.Marshal(metadataProto)
	if err != nil {
		return err
	}

	// Phase 5: V2Header
	header := &savev2proto.V2Header{
		MetadataSize:    uint32(len(metadataBytes)),
		StringsBlobSize: uint32(len(stringsBlob)),
		ZonesSize:       uint32(len(zonesBytes)),
	}
	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return err
	}

	// Phase 6: Write everything sequentially
	if err := binary.Write(w, binary.LittleEndian, uint32(len(headerBytes))); err != nil {
		return err
	}
	if _, err := w.Write(headerBytes); err != nil {
		return err
	}
	if _, err := w.Write(metadataBytes); err != nil {
		return err
	}
	if _, err := w.Write(stringsBlob); err != nil {
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

// buildStringsBlob concatenates all dedup'd strings into a single blob.
// All dedup maps share the same underlying map, so we only need to iterate one.
func buildStringsBlob(dedup *stringsDedup) []byte {
	dm := dedup.names // shared across all categories
	blob := make([]byte, dm.nextOff)
	for s, off := range dm.m {
		if off.length == 0 {
			continue
		}
		copy(blob[off.offset:off.offset+int64(off.length)], s)
	}
	return blob
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
