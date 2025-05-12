package savev1

import (
	"encoding/binary"
	"io"

	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	"google.golang.org/protobuf/proto"
)

func Load(r io.Reader) (Cache, error) {
	cache := Cache{}

	var headerSize uint32
	err := binary.Read(r, binary.LittleEndian, &headerSize)
	if err != nil {
		return cache, err
	}

	var header saveproto.CacheHeader
	err = readToProto(r, headerSize, &header)
	if err != nil {
		return cache, err
	}

	var metadata saveproto.CacheMetadata
	err = readToProto(r, header.MetadataSize, &metadata)
	if err != nil {
		return cache, err
	}

	var stringsCache saveproto.StringsCache
	err = readToProto(r, header.StringsCacheSize, &stringsCache)
	if err != nil {
		return cache, err
	}

	cache.Streets = stringsCache.Streets
	cache.Cities = stringsCache.Cities
	cache.Regions = stringsCache.Regions

	var pointsBlob saveproto.PointsBlob
	err = readToProto(r, header.PointsBlobSize, &pointsBlob)
	if err != nil {
		return cache, err
	}
	for _, p := range pointsBlob.Points {
		cache.Points = append(cache.Points, mapPoint(p))
	}

	for {
		err = readToProto(r, header.PointsBlobSize, &pointsBlob)
		if err == io.EOF {
			break
		}
		if err != nil {
			return cache, err
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
	_, err := r.Read(buf)
	if err != nil {
		return err
	}
	err = proto.Unmarshal(buf, val)
	if err != nil {
		return err
	}
	return nil
}
