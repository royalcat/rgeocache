package savev1

import (
	"encoding/binary"
	"io"

	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	"google.golang.org/protobuf/proto"
)

func Save(w io.Writer, cache Cache) error {
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

	// Prepare metadata (currently empty in the load function, keeping it for future use)
	metadata := &saveproto.CacheMetadata{}
	metadataBytes, err := proto.Marshal(metadata)
	if err != nil {
		return err
	}

	// Prepare points blob
	// We'll write points in chunks of 1000
	const pointsChunkSize = 1000

	// Create header
	header := &saveproto.CacheHeader{
		MetadataSize:     uint32(len(metadataBytes)),
		StringsCacheSize: uint32(len(stringsCacheBytes)),
		PointsBlobSize:   0, // We'll calculate this with the first blob
	}

	// Write points blobs
	var pointsBlobs [][]byte
	for i := 0; i < len(cache.Points); i += pointsChunkSize {
		end := i + pointsChunkSize
		if end > len(cache.Points) {
			end = len(cache.Points)
		}

		chunk := cache.Points[i:end]
		pointsProto := make([]*saveproto.Point, len(chunk))
		for j, point := range chunk {
			pointsProto[j] = &saveproto.Point{
				Latitude:    point.Lat,
				Longitude:   point.Lon,
				Name:        point.Name,
				Street:      point.Street,
				HouseNumber: point.HouseNumber,
				City:        point.City,
				Region:      point.Region,
			}
		}

		pointsBlob := &saveproto.PointsBlob{
			Points: pointsProto,
		}

		blobBytes, err := proto.Marshal(pointsBlob)
		if err != nil {
			return err
		}

		pointsBlobs = append(pointsBlobs, blobBytes)

		// Set the points blob size in the header if this is the first chunk
		if i == 0 {
			header.PointsBlobSize = uint32(len(blobBytes))
		}
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

	return nil
}
