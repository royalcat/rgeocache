package savev1

import (
	"encoding/binary"
	"io"
	"iter"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	"google.golang.org/protobuf/proto"
)

func Save(w io.Writer, points iter.Seq[cachemodel.Point], zones iter.Seq[cachemodel.Zone], metadata cachemodel.Metadata) error {
	cache := cacheFromPoints(points, zones, metadata)

	// Prepare strings cache
	stringsCache := &saveproto.StringsCache{
		Streets: cache.StreetsNames,
		Cities:  cache.CitiesNames,
		Regions: cache.ZonesNames,
	}
	stringsCacheBytes, err := proto.Marshal(stringsCache)
	if err != nil {
		return err
	}

	protoMetadata := &saveproto.CacheMetadata{
		Version:     cache.Version,
		DateCreated: cache.DateCreated,
		Locale:      cache.Locale,
	}
	metadataBytes, err := proto.Marshal(protoMetadata)
	if err != nil {
		return err
	}

	// Prepare points blob
	// We'll write points in chunks of 1000
	const blobChunkSize = 100

	// Create header
	header := &saveproto.CacheHeader{
		MetadataSize:     uint32(len(metadataBytes)),
		StringsCacheSize: uint32(len(stringsCacheBytes)),
		PointsBlobSizes:  []uint32{},
	}

	// Write points blobs
	var pointsBlobs [][]byte
	for i := 0; i < len(cache.Points); i += blobChunkSize {
		end := min(i+blobChunkSize, len(cache.Points))

		pointsBlob := &saveproto.PointsBlob{
			Points: slicePtr(cache.Points[i:end]),
		}

		blobBytes, err := proto.Marshal(pointsBlob)
		if err != nil {
			return err
		}

		pointsBlobs = append(pointsBlobs, blobBytes)

		header.PointsBlobSizes = append(header.PointsBlobSizes, uint32(len(blobBytes)))
	}

	var zoneProtos = iter.Seq[*saveproto.ZonesBlob](func(yield func(*saveproto.ZonesBlob) bool) {
		for i := 0; i < len(cache.Regions); i += blobChunkSize {
			end := min(i+blobChunkSize, len(cache.Regions))
			zoneProto := &saveproto.ZonesBlob{
				Type:  saveproto.ZoneType_ZONE_TYPE_REGION,
				Zones: cache.Regions[i:end],
			}

			if !yield(zoneProto) {
				return
			}
		}

		for i := 0; i < len(cache.Countries); i += blobChunkSize {
			end := min(i+blobChunkSize, len(cache.Countries))
			zoneProto := &saveproto.ZonesBlob{
				Type:  saveproto.ZoneType_ZONE_TYPE_COUNTRY,
				Zones: cache.Countries[i:end],
			}
			if !yield(zoneProto) {
				return
			}
		}

	})

	var zoneBlobs [][]byte
	for zoneProto := range zoneProtos {
		blobBytes, err := proto.Marshal(zoneProto)
		if err != nil {
			return err
		}

		zoneBlobs = append(zoneBlobs, blobBytes)
		header.ZonesBlobSizes = append(header.ZonesBlobSizes, uint32(len(blobBytes)))
	}

	// Serialize header
	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return err
	}

	// Write header size
	err = binary.Write(w, binary.LittleEndian, uint32(len(headerBytes)))
	if err != nil {
		return err
	}

	// Write header
	_, err = w.Write(headerBytes)
	if err != nil {
		return err
	}

	// Write metadata
	_, err = w.Write(metadataBytes)
	if err != nil {
		return err
	}

	// Write strings cache
	_, err = w.Write(stringsCacheBytes)
	if err != nil {
		return err
	}

	// Write points blobs
	for _, blobBytes := range pointsBlobs {
		_, err = w.Write(blobBytes)
		if err != nil {
			return err
		}
	}

	for _, blobBytes := range zoneBlobs {
		_, err = w.Write(blobBytes)
		if err != nil {
			return err
		}
	}

	return nil
}

func mapZoneType(t cachemodel.ZoneType) saveproto.ZoneType {
	switch t {
	case cachemodel.ZoneRegion:
		return saveproto.ZoneType_ZONE_TYPE_REGION
	case cachemodel.ZoneCountry:
		return saveproto.ZoneType_ZONE_TYPE_COUNTRY
	default:
		return saveproto.ZoneType_ZONE_TYPE_UNSPECIFIED
	}
}

func slicePtr[T any](slice []T) []*T {
	ptrs := make([]*T, len(slice))
	for i, v := range slice {
		ptrs[i] = &v
	}
	return ptrs
}
