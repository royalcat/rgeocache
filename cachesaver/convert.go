package cachesaver

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
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

// V1RoadSpacingMeters is the default spacing v1 road points are thinned to. It
// matches the generator's pre-road-graph HighwayPointsDistance and the graph
// edge cap, so a v1 cache keeps roughly one road point per 150 m of street.
const V1RoadSpacingMeters = 150.0

// metersPerDegreeLat is the mean length of one degree of latitude.
const metersPerDegreeLat = 111_320.0

// V1RoadMode selects which road points a v1 cache keeps.
type V1RoadMode string

const (
	// V1RoadsMajor keeps road points on graph edges of class motorway..tertiary,
	// the classes the pre-road-graph v1 cache contained.
	V1RoadsMajor V1RoadMode = "major"
	// V1RoadsAll keeps every road point that carries a street or a name.
	V1RoadsAll V1RoadMode = "all"
	// V1RoadsNone drops every road point.
	V1RoadsNone V1RoadMode = "none"
)

// ConvertOptions controls the point filter applied while converting a v2 cache
// to v1. v1 has no road graph and no geo type byte, so points that only exist
// to carry graph geometry are dropped.
type ConvertOptions struct {
	// Roads selects the road points to keep. The empty value means V1RoadsMajor.
	Roads V1RoadMode
	// RoadSpacingMeters thins road points so that at most one point per street
	// and cell of this size survives. Zero disables thinning. Negative values
	// are rejected.
	RoadSpacingMeters float64
}

// DefaultConvertOptions returns the conversion options used by the CLI: major
// roads thinned to one point per 150 m of street.
func DefaultConvertOptions() ConvertOptions {
	return ConvertOptions{
		Roads:             V1RoadsMajor,
		RoadSpacingMeters: V1RoadSpacingMeters,
	}
}

// ConvertV2ToV1 writes a v1 cache from a finished v2 cache using
// [DefaultConvertOptions]. It is a streaming conversion: the v2 file is
// memory-mapped and the v1 body is written in two sequential passes (one to
// decide which points to keep and size their blobs, one to write them), so heap
// usage is O(unique strings + kept road cells) rather than O(points).
//
// v1 has no graph section and no explicit geo type. It keeps the same zones and
// metadata as the v2 cache, and only the address-bearing points a
// reverse-geocoding consumer needs.
func ConvertV2ToV1(v2Path string, w io.Writer) error {
	return ConvertV2ToV1WithOptions(v2Path, w, DefaultConvertOptions())
}

// ConvertV2ToV1WithOptions is [ConvertV2ToV1] with explicit point-filter
// options. An existing v2 cache can be re-converted without regenerating it.
func ConvertV2ToV1WithOptions(v2Path string, w io.Writer, opts ConvertOptions) error {
	opts, err := normalizeConvertOptions(opts)
	if err != nil {
		return err
	}
	log := slog.Default()

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

	numPoints := res.DiskBush.NumPoints()
	filter, err := newV1PointFilter(v2Path, ra, numPoints, opts, log)
	if err != nil {
		return err
	}

	// Pass 1: decide which points to keep, record them in a bitmap for pass 2,
	// and size every 100-point blob without keeping its bytes.
	keep := make([]uint64, (numPoints+63)/64)
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
	iterErr := res.DiskBush.ForEachPos(func(pos int, p kdbush.Point[savev2.V2PointData]) bool {
		if !filter.keep(pos, p) {
			return true
		}
		setV1Bit(keep, pos)
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
	kept := filter.kept
	if want := (kept + v1BlobChunk - 1) / v1BlobChunk; len(pointSizes) != want {
		return fmt.Errorf("convert: sized %d point blobs, expected %d", len(pointSizes), want)
	}
	filter.release()

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

	// Pass 2: marshal and write the blobs in the same order.
	chunk = chunk[:0]
	written := 0
	iterErr = res.DiskBush.ForEachPos(func(pos int, p kdbush.Point[savev2.V2PointData]) bool {
		if !v1BitSet(keep, pos) {
			return true
		}
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
	if err := bw.Flush(); err != nil {
		return err
	}

	log.Info("Wrote v1 cache",
		"points", kept,
		"road_mode", string(opts.Roads),
		"road_spacing_m", opts.RoadSpacingMeters,
		"dropped_nameless_roads", filter.droppedNameless,
		"dropped_minor_roads", filter.droppedMinor,
		"dropped_roads", filter.droppedRoadless,
		"thinned_roads", filter.thinned,
	)
	return nil
}

func normalizeConvertOptions(opts ConvertOptions) (ConvertOptions, error) {
	switch opts.Roads {
	case "":
		opts.Roads = V1RoadsMajor
	case V1RoadsMajor, V1RoadsAll, V1RoadsNone:
	default:
		return ConvertOptions{}, fmt.Errorf("convert: unknown v1 road mode %q", opts.Roads)
	}
	if opts.RoadSpacingMeters < 0 {
		return ConvertOptions{}, fmt.Errorf("convert: v1 road spacing must not be negative: %v", opts.RoadSpacingMeters)
	}
	return opts, nil
}

// v1PointFilter decides whether a v2 point belongs in the v1 cache.
type v1PointFilter struct {
	roads   V1RoadMode
	spacing float64
	// major marks the sorted positions of points on a road graph edge of class
	// motorway..tertiary. It is nil when the v2 cache has no graph section, in
	// which case the major mode keeps every address-bearing road point.
	major []uint64
	seen  map[v1RoadCell]struct{}

	kept            int
	droppedNameless int
	droppedMinor    int
	droppedRoadless int
	thinned         int
}

func newV1PointFilter(
	v2Path string,
	ra *mmap.ReaderAt,
	numPoints int,
	opts ConvertOptions,
	log *slog.Logger,
) (*v1PointFilter, error) {
	f := &v1PointFilter{roads: opts.Roads, spacing: opts.RoadSpacingMeters}
	if f.spacing > 0 {
		f.seen = make(map[v1RoadCell]struct{})
	}
	if opts.Roads != V1RoadsMajor {
		return f, nil
	}

	major, ok, err := loadMajorRoadPositions(ra, int64(ra.Len()), numPoints)
	if err != nil {
		return nil, err
	}
	if !ok {
		log.Warn("v2 cache has no road graph section; keeping all named road points",
			"path", v2Path, "road_mode", string(opts.Roads))
		return f, nil
	}
	f.major = major
	return f, nil
}

// release drops the thinning set after pass 1; pass 2 uses the keep bitmap.
func (f *v1PointFilter) release() {
	f.seen = nil
}

// keep reports whether the point at sorted position pos belongs in the v1
// cache. Positions are the same ones graph edge endpoints use.
func (f *v1PointFilter) keep(pos int, p kdbush.Point[savev2.V2PointData]) bool {
	if p.Data.Weight != roadPointWeight {
		f.kept++
		return true
	}
	if p.Data.StreetID == 0 && p.Data.NameID == 0 {
		f.droppedNameless++
		return false
	}
	switch f.roads {
	case V1RoadsNone:
		f.droppedRoadless++
		return false
	case V1RoadsMajor:
		if f.major != nil && !v1BitSet(f.major, pos) {
			f.droppedMinor++
			return false
		}
	}
	if f.spacing > 0 {
		key := v1RoadCellKey(p, f.spacing)
		if _, ok := f.seen[key]; ok {
			f.thinned++
			return false
		}
		f.seen[key] = struct{}{}
	}
	f.kept++
	return true
}

// v1RoadCell identifies one thinning cell of a street.
type v1RoadCell struct {
	road  uint32
	cellX int32
	cellY int32
}

// v1RoadCellKey returns the street and the 150 m grid cell of a road point.
// p.X is longitude and p.Y is latitude; the longitude step shrinks with
// cos(latitude) so cells stay roughly square in metres.
func v1RoadCellKey(p kdbush.Point[savev2.V2PointData], spacingMeters float64) v1RoadCell {
	road := p.Data.StreetID
	if road == 0 {
		road = p.Data.NameID
	}
	latStep := spacingMeters / metersPerDegreeLat
	lonStep := latStep / math.Max(math.Cos(p.Y*math.Pi/180), 0.01)
	return v1RoadCell{
		road:  road,
		cellX: int32(math.Floor(p.X / lonStep)),
		cellY: int32(math.Floor(p.Y / latStep)),
	}
}

// loadMajorRoadPositions marks every point that is an endpoint of a graph edge
// of class motorway..tertiary. The second return value is false when the cache
// has no graph section.
func loadMajorRoadPositions(ra *mmap.ReaderAt, size int64, numPoints int) ([]uint64, bool, error) {
	graph, err := savev2.FindGraphSection(ra, size)
	if errors.Is(err, savev2.ErrNoGraphSection) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	major := make([]uint64, (numPoints+63)/64)
	var cbErr error
	err = graph.ForEachEdge(ra, func(e savev2.GraphEdgeRecord) bool {
		if e.Class < cachemodel.GraphClassMotorway || e.Class > cachemodel.GraphClassTertiary {
			return true
		}
		if int(e.FromPos) >= numPoints || int(e.ToPos) >= numPoints {
			cbErr = fmt.Errorf(
				"convert: graph edge references points %d/%d, cache has %d points",
				e.FromPos, e.ToPos, numPoints,
			)
			return false
		}
		setV1Bit(major, int(e.FromPos))
		setV1Bit(major, int(e.ToPos))
		return true
	})
	if err != nil {
		return nil, false, err
	}
	if cbErr != nil {
		return nil, false, cbErr
	}
	return major, true, nil
}

func setV1Bit(bits []uint64, pos int) {
	bits[pos>>6] |= 1 << (uint(pos) & 63)
}

func v1BitSet(bits []uint64, pos int) bool {
	return bits[pos>>6]&(1<<(uint(pos)&63)) != 0
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
