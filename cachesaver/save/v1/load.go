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

		type chunk struct {
			buf []byte
			err error
		}

		chunkChan := make(chan chunk, 10)

		go func() {
			defer close(chunkChan)
			for i := 0; i < len(header.PointsBlobSizes); i++ {
				if stopped.Load() {
					return
				}

				buf := make([]byte, header.PointsBlobSizes[i])
				n, err := io.ReadAtLeast(r, buf, int(header.PointsBlobSizes[i]))
				if err != nil {
					chunkChan <- chunk{nil, fmt.Errorf("failed to read points blob at index %d: %w", i, err)}
					return
				}
				chunkChan <- chunk{buf[:n], nil}
			}
		}()

		type pointsBlobElem struct {
			points []*saveproto.Point
			err    error
		}
		pointsBlobChan := make(chan pointsBlobElem, 10)

		go func() {
			defer close(pointsBlobChan)
			for blob := range chunkChan {
				if stopped.Load() {
					return
				}

				if blob.err != nil {
					pointsBlobChan <- pointsBlobElem{nil, blob.err}
					continue
				}

				var pointsBlob saveproto.PointsBlob
				err = proto.Unmarshal(blob.buf, &pointsBlob)
				if err != nil {
					pointsBlobChan <- pointsBlobElem{nil, fmt.Errorf("failed to unmarshal points blob: %w", err)}
					return
				}

				pointsBlobChan <- pointsBlobElem{pointsBlob.Points, nil}
			}
		}()

		type point struct {
			point cachemodel.Point
			err   error
		}
		outChan := make(chan point, 100)

		go func() {
			defer close(outChan)
			for pointsBlob := range pointsBlobChan {
				if stopped.Load() {
					return
				}

				if pointsBlob.err != nil {
					outChan <- point{cachemodel.Point{}, pointsBlob.err}
					continue
				}

				for _, p := range pointsBlob.points {
					outChan <- point{mapPoint(p, &stringsCache), nil}
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
	czt := cachemodel.ZoneType(0)
	switch t {
	case saveproto.ZoneType_ZONE_TYPE_REGION:
		czt = cachemodel.ZoneRegion
	case saveproto.ZoneType_ZONE_TYPE_COUNTRY:
		czt = cachemodel.ZoneCountry
	default:
		return cachemodel.Zone{}, false
	}

	return cachemodel.Zone{
		Type:    czt,
		Name:    unique.Make(stringsCache.Regions[z.Name]),
		Bounds:  mapBoundsToOrb(z.Bounds),
		Polygon: mapMultiPolygonToOrb(z.MultiPolygon),
	}, true
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
