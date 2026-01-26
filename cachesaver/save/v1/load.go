package savev1

import (
	"encoding/binary"
	"fmt"
	"io"
	"iter"

	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	"google.golang.org/protobuf/proto"
)

func Load(r io.Reader) (iter.Seq2[Point, error], *saveproto.StringsCache, *saveproto.CacheMetadata, error) {

	var headerSize uint32
	err := binary.Read(r, binary.LittleEndian, &headerSize)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read header size: %w", err)
	}

	var header saveproto.CacheHeader
	err = readToProto(r, headerSize, &header)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read header: %w", err)
	}

	var metadata saveproto.CacheMetadata
	err = readToProto(r, header.MetadataSize, &metadata)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read metadata: %w", err)
	}

	var stringsCache saveproto.StringsCache
	err = readToProto(r, header.StringsCacheSize, &stringsCache)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read strings cache: %w", err)
	}

	pointsIter := func(yield func(Point, error) bool) {
		var pointsBlob saveproto.PointsBlob
		for i := 0; i < len(header.PointsBlobSizes); i++ {
			err = readToProto(r, header.PointsBlobSizes[i], &pointsBlob)
			if err == io.EOF {
				break
			}
			if err != nil {
				if !yield(Point{}, fmt.Errorf("failed to read points blob at index %d: %w", i, err)) {
					return
				} else {
					continue
				}
			}
			for _, p := range pointsBlob.Points {
				if !yield(mapPoint(p), nil) {
					return
				}
			}
		}
	}

	return pointsIter, &stringsCache, &metadata, nil
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
		Weight:      uint8(p.Weight),
	}
}

func readToProto(r io.Reader, size uint32, val proto.Message) error {
	buf := make([]byte, size)
	n, err := io.ReadAtLeast(r, buf, int(size))
	if err != nil {
		return fmt.Errorf("failed to read %d bytes: %w", size, err)
	}
	err = proto.Unmarshal(buf[:n], val)
	if err != nil {
		return fmt.Errorf("failed to unmarshal %d bytes: %w", size, err)
	}
	return nil
}
