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
		Streets: cache.Streets,
		Cities:  cache.Cities,
		Regions: cache.Regions,
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
	const pointsChunkSize = 100

	// Create header
	header := &saveproto.CacheHeader{
		MetadataSize:     uint32(len(metadataBytes)),
		StringsCacheSize: uint32(len(stringsCacheBytes)),
		PointsBlobSizes:  []uint32{},
	}

	// Write points blobs
	var pointsBlobs [][]byte
	for i := 0; i < len(cache.Points); i += pointsChunkSize {
		end := min(i+pointsChunkSize, len(cache.Points))

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

	var zonesBlobs [][]byte
	for i := 0; i < len(cache.Zones); i += pointsChunkSize {
		end := min(i+pointsChunkSize, len(cache.Zones))

		zonesBlob := &saveproto.ZonesBlob{
			Type:  saveproto.ZoneType_ZONE_TYPE_REGION,
			Zones: slicePtr(cache.Zones[i:end]),
		}

		blobBytes, err := proto.Marshal(zonesBlob)
		if err != nil {
			return err
		}

		zonesBlobs = append(zonesBlobs, blobBytes)

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

	for _, blobBytes := range zonesBlobs {
		_, err = w.Write(blobBytes)
		if err != nil {
			return err
		}
	}

	return nil
}

func slicePtr[T any](slice []T) []*T {
	ptrs := make([]*T, len(slice))
	for i, v := range slice {
		ptrs[i] = &v
	}
	return ptrs
}
