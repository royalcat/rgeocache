package cachesaver

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1 "github.com/royalcat/rgeocache/cachesaver/save/v1"
	savev1proto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/kdbush"
	"golang.org/x/exp/mmap"
	"google.golang.org/protobuf/proto"
)

const v1BlobChunk = 100

// ConvertV2ToV1 writes a v1 cache from a finished v2 cache. It is a streaming
// conversion: the v2 file is memory-mapped and the v1 body is written in two
// sequential passes (one to size the point blobs, one to write them), so heap
// usage is O(unique strings) rather than O(points).
//
// v1 has no graph section and no explicit geo type. It keeps the same points,
// zones and metadata, which is everything a reverse-geocoding consumer needs.
func ConvertV2ToV1(v2Path string, w io.Writer) error {
	ra, err := mmap.Open(v2Path)
	if err != nil {
		return fmt.Errorf("convert: opening %s: %w", v2Path, err)
	}
	defer func() { _ = ra.Close() }()

	res, err := savev2.LoadMmap(ra)
	if err != nil {
		return fmt.Errorf("convert: loading %s: %w", v2Path, err)
	}

	if _, err := w.Write(MAGIC_BYTES); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, savev1.COMPATIBILITY_LEVEL); err != nil {
		return err
	}

	strings, err := res.Strings()
	if err != nil {
		return err
	}

	// v1 indexes street, city and region through separate tables. The v2 table
	// ids are global, so the same table can back all three; zone names are
	// appended to the region table.
	regions := append([]string(nil), strings...)
	regionID := make(map[string]uint32, len(regions))
	for id, s := range regions {
		regionID[s] = uint32(id)
	}
	for _, z := range res.Zones {
		name := z.Name.Value()
		if _, ok := regionID[name]; !ok {
			regionID[name] = uint32(len(regions))
			regions = append(regions, name)
		}
	}

	stringsCache := &savev1proto.StringsCache{
		Streets: strings,
		Cities:  strings,
		Regions: regions,
	}
	stringsBytes, err := proto.Marshal(stringsCache)
	if err != nil {
		return err
	}

	meta := res.Metadata
	metadataBytes, err := proto.Marshal(&savev1proto.CacheMetadata{
		Version:     meta.Version,
		DateCreated: meta.DateCreated.UTC().Format(time.RFC3339),
		Locale:      meta.Locale,
	})
	if err != nil {
		return err
	}

	// Zone blobs are tiny; marshal them up front because their sizes belong to
	// the header written before the point blobs.
	zoneBlobs, zoneSizes, err := marshalV1Zones(res.Zones, regionID)
	if err != nil {
		return err
	}

	// Pass 1: size every 100-point blob without keeping its bytes.
	numPoints := res.DiskBush.NumPoints()
	pointSizes := make([]uint32, 0, (numPoints+v1BlobChunk-1)/v1BlobChunk)
	chunk := make([]*savev1proto.Point, 0, v1BlobChunk)
	flushSize := func() error {
		if len(chunk) == 0 {
			return nil
		}
		pointSizes = append(pointSizes, uint32(proto.Size(&savev1proto.PointsBlob{Points: chunk})))
		chunk = chunk[:0]
		return nil
	}
	var cbErr error
	iterErr := res.DiskBush.ForEach(func(p kdbush.Point[savev2.V2PointData]) bool {
		sp, err := toV1Point(p, strings)
		if err != nil {
			cbErr = err
			return false
		}
		chunk = append(chunk, sp)
		if len(chunk) == v1BlobChunk {
			if err := flushSize(); err != nil {
				cbErr = err
				return false
			}
		}
		return true
	})
	if iterErr != nil {
		return iterErr
	}
	if cbErr != nil {
		return cbErr
	}
	if err := flushSize(); err != nil {
		return err
	}
	if want := (numPoints + v1BlobChunk - 1) / v1BlobChunk; len(pointSizes) != want {
		return fmt.Errorf("convert: sized %d point blobs, expected %d", len(pointSizes), want)
	}

	header := &savev1proto.CacheHeader{
		MetadataSize:     uint32(len(metadataBytes)),
		StringsCacheSize: uint32(len(stringsBytes)),
		PointsBlobSizes:  pointSizes,
		ZonesBlobSizes:   zoneSizes,
	}
	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return err
	}

	bw := bufio.NewWriterSize(w, 1<<20)
	if err := binary.Write(bw, binary.LittleEndian, uint32(len(headerBytes))); err != nil {
		return err
	}
	for _, b := range [][]byte{headerBytes, metadataBytes, stringsBytes} {
		if _, err := bw.Write(b); err != nil {
			return err
		}
	}

	// Pass 2: marshal and write the point blobs in the same order.
	chunk = chunk[:0]
	written := 0
	iterErr = res.DiskBush.ForEach(func(p kdbush.Point[savev2.V2PointData]) bool {
		sp, err := toV1Point(p, strings)
		if err != nil {
			cbErr = err
			return false
		}
		chunk = append(chunk, sp)
		if len(chunk) == v1BlobChunk {
			b, err := proto.Marshal(&savev1proto.PointsBlob{Points: chunk})
			if err != nil {
				cbErr = err
				return false
			}
			if _, err := bw.Write(b); err != nil {
				cbErr = err
				return false
			}
			chunk = chunk[:0]
			written++
		}
		return true
	})
	if iterErr != nil {
		return iterErr
	}
	if cbErr != nil {
		return cbErr
	}
	if len(chunk) > 0 {
		b, err := proto.Marshal(&savev1proto.PointsBlob{Points: chunk})
		if err != nil {
			return err
		}
		if _, err := bw.Write(b); err != nil {
			return err
		}
		written++
	}
	if written != len(pointSizes) {
		return fmt.Errorf("convert: wrote %d point blobs, expected %d", written, len(pointSizes))
	}

	for _, b := range zoneBlobs {
		if _, err := bw.Write(b); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func toV1Point(p kdbush.Point[savev2.V2PointData], strings []string) (*savev1proto.Point, error) {
	str := func(id uint32) (string, error) {
		if int(id) >= len(strings) {
			return "", fmt.Errorf("convert: string id %d out of range (%d strings)", id, len(strings))
		}
		return strings[id], nil
	}
	name, err := str(p.Data.NameID)
	if err != nil {
		return nil, err
	}
	house, err := str(p.Data.HouseNumberID)
	if err != nil {
		return nil, err
	}
	if int(p.Data.StreetID) >= len(strings) || int(p.Data.CityID) >= len(strings) || int(p.Data.RegionID) >= len(strings) {
		return nil, fmt.Errorf("convert: point address references an unknown string id")
	}
	// The v1 format stores X in the field named Latitude and Y in Longitude;
	// its loader reverses the naming. Keep the same convention so the values
	// round-trip.
	return &savev1proto.Point{
		Latitude:    p.X,
		Longitude:   p.Y,
		Name:        name,
		Street:      p.Data.StreetID,
		HouseNumber: house,
		City:        p.Data.CityID,
		Region:      p.Data.RegionID,
		Weight:      uint32(p.Data.Weight),
	}, nil
}

func marshalV1Zones(zones []cachemodel.Zone, regionID map[string]uint32) ([][]byte, []uint32, error) {
	var blobs [][]byte
	var sizes []uint32
	for _, zoneType := range []cachemodel.ZoneType{cachemodel.ZoneRegion, cachemodel.ZoneCountry} {
		var chunk []*savev1proto.Zone
		flush := func() error {
			if len(chunk) == 0 {
				return nil
			}
			b, err := proto.Marshal(&savev1proto.ZonesBlob{
				Type:  savev1.ZoneTypeProto(zoneType),
				Zones: chunk,
			})
			if err != nil {
				return err
			}
			blobs = append(blobs, b)
			sizes = append(sizes, uint32(len(b)))
			chunk = chunk[:0]
			return nil
		}
		for _, z := range zones {
			if z.Type != zoneType {
				continue
			}
			nameID, ok := regionID[z.Name.Value()]
			if !ok {
				return nil, nil, fmt.Errorf("convert: zone name %q is missing from the string table", z.Name.Value())
			}
			chunk = append(chunk, savev1.ZoneProto(z, nameID))
			if len(chunk) == v1BlobChunk {
				if err := flush(); err != nil {
					return nil, nil, err
				}
			}
		}
		if err := flush(); err != nil {
			return nil, nil, err
		}
	}
	return blobs, sizes, nil
}
