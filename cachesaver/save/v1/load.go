package savev1

import (
	"encoding/binary"
	"fmt"
	"io"

	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	"google.golang.org/protobuf/proto"
)

func Load(r io.Reader) (Cache, error) {
	cache := Cache{}

	var headerSize uint32
	err := binary.Read(r, binary.LittleEndian, &headerSize)
	if err != nil {
		return cache, fmt.Errorf("failed to read header size: %w", err)
	}

	var header saveproto.CacheHeader
	err = readToProto(r, headerSize, &header)
	if err != nil {
		return cache, fmt.Errorf("failed to read header: %w", err)
	}

	var metadata saveproto.CacheMetadata
	err = readToProto(r, header.MetadataSize, &metadata)
	if err != nil {
		return cache, fmt.Errorf("failed to read metadata: %w", err)
	}

	var stringsCache saveproto.StringsCache
	err = readToProto(r, header.StringsCacheSize, &stringsCache)
	if err != nil {
		return cache, fmt.Errorf("failed to read strings cache: %w", err)
	}

	cache.Streets = stringsCache.Streets
	cache.Cities = stringsCache.Cities
	cache.Regions = stringsCache.Regions

	var pointsBlob saveproto.PointsBlob
	err = readToProto(r, header.PointsBlobSizes[0], &pointsBlob)
	if err != nil {
		return cache, fmt.Errorf("failed to read points blob at index 0: %w", err)
	}
	for _, p := range pointsBlob.Points {
		cache.Points = append(cache.Points, mapPoint(p))
	}

	for i := 1; i < len(header.PointsBlobSizes); i++ {
		err = readToProto(r, header.PointsBlobSizes[i], &pointsBlob)
		if err == io.EOF {
			break
		}
		if err != nil {
			return cache, fmt.Errorf("failed to read points blob at index %d: %w", i, err)
		}
		for _, p := range pointsBlob.Points {
			cache.Points = append(cache.Points, mapPoint(p))
		}
	}

	return cache, nil
}

func mapPoint(p *saveproto.Point) Point {
	return Point{
		Lat:         p.Latitude,
		Lon:         p.Longitude,
		Name:        p.Name,
		Street:      p.Street,
		HouseNumber: p.HouseNumber,
		City:        p.City,
		Region:      p.Region,
	}
}

func readToProto(r io.Reader, size uint32, val proto.Message) error {
	buf := make([]byte, size)
	n, err := r.Read(buf)
	if err != nil {
		return fmt.Errorf("failed to read %d bytes: %w", size, err)
	}
	err = proto.Unmarshal(buf[:n], val)
	if err != nil {
		return fmt.Errorf("failed to unmarshal %d bytes: %w", size, err)
	}
	return nil
}
