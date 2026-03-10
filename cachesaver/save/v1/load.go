package savev1

import (
	"encoding/binary"
	"fmt"
	"io"
	"iter"
	"sync/atomic"
	"time"
	"unique"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	saveproto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	"google.golang.org/protobuf/proto"
)

func Load(r io.Reader) (iter.Seq2[cachemodel.Point, error], iter.Seq2[cachemodel.Zone, error], *cachemodel.Metadata, error) {

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

	PrintCacheSizeAnalysis(&header)

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

	var pointsConsumed bool

	pointsIter := func(yield func(cachemodel.Point, error) bool) {
		defer func() {
			pointsConsumed = true
		}()

		var stopped atomic.Bool

		blobChan := make(chan struct {
			buf []byte
			err error
		})

		go func() {
			defer close(blobChan)
			for i := 0; i < len(header.PointsBlobSizes); i++ {
				if stopped.Load() {
					return
				}

				buf := make([]byte, header.PointsBlobSizes[i])
				n, err := io.ReadAtLeast(r, buf, int(header.PointsBlobSizes[i]))
				if err != nil {
					blobChan <- struct {
						buf []byte
						err error
					}{nil, fmt.Errorf("failed to read points blob at index %d: %w", i, err)}
					return
				}
				blobChan <- struct {
					buf []byte
					err error
				}{buf[:n], nil}
			}
		}()

		outChan := make(chan struct {
			point cachemodel.Point
			err   error
		})

		go func() {
			defer close(outChan)
			for blob := range blobChan {
				if stopped.Load() {
					return
				}

				if blob.err != nil {
					outChan <- struct {
						point cachemodel.Point
						err   error
					}{cachemodel.Point{}, blob.err}
					continue
				}

				var pointsBlob saveproto.PointsBlob
				err = proto.Unmarshal(blob.buf, &pointsBlob)
				if err != nil {
					outChan <- struct {
						point cachemodel.Point
						err   error
					}{cachemodel.Point{}, fmt.Errorf("failed to unmarshal points blob: %w", err)}
					return
				}

				for _, p := range pointsBlob.Points {
					outChan <- struct {
						point cachemodel.Point
						err   error
					}{mapPoint(p, &stringsCache), nil}
				}
			}
		}()

		for out := range outChan {
			if !yield(out.point, out.err) {
				stopped.Store(true)
				return
			}
		}
	}

	zonesIter := func(yield func(cachemodel.Zone, error) bool) {
		if !pointsConsumed {
			panic("points should be consumed before zones")
		}

		var zonesBlob saveproto.ZonesBlob
		for i := 0; i < len(header.ZonesBlobSizes); i++ {
			err = readToProto(r, header.ZonesBlobSizes[i], &zonesBlob)
			if err == io.EOF {
				break
			}
			if err != nil {
				if !yield(cachemodel.Zone{}, fmt.Errorf("failed to read zones blob at index %d: %w", i, err)) {
					return
				} else {
					continue
				}
			}
			for _, z := range zonesBlob.Zones {
				zone, ok := mapZone(zonesBlob.Type, z, &stringsCache)
				if !ok {
					if !yield(cachemodel.Zone{}, fmt.Errorf("failed to map zone at index %d", i)) {
						return
					} else {
						continue
					}
				}

				if !yield(zone, nil) {
					return
				}
			}
		}
	}

	dateCreated, err := time.Parse(time.RFC3339, metadata.DateCreated)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to parse date created: %w", err)
	}

	meta := cachemodel.Metadata{
		Version:     metadata.Version,
		Locale:      metadata.Locale,
		DateCreated: dateCreated,
	}

	return pointsIter, zonesIter, &meta, nil
}

func mapZone(t saveproto.ZoneType, z *saveproto.Zone, stringsCache *saveproto.StringsCache) (cachemodel.Zone, bool) {
	switch t {
	case saveproto.ZoneType_ZONE_TYPE_REGION:
		return cachemodel.Zone{
			Name:    unique.Make(stringsCache.Regions[z.Name]),
			Bounds:  mapBoundsToOrb(z.Bounds),
			Polygon: mapMultiPolygonToOrb(z.MultiPolygon),
		}, true
	default:
		return cachemodel.Zone{}, false
	}

}

func mapPoint(p *saveproto.Point, stringsCache *saveproto.StringsCache) cachemodel.Point {
	return cachemodel.Point{
		X: p.Latitude,
		Y: p.Longitude,
		Data: cachemodel.Info{
			Name:        unique.Make(p.Name),
			Street:      unique.Make(stringsCache.Streets[p.Street]),
			HouseNumber: unique.Make(p.HouseNumber),
			City:        unique.Make(stringsCache.Cities[p.City]),
			Region:      unique.Make(stringsCache.Regions[p.Region]),
			Weight:      uint8(p.Weight),
		},
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
