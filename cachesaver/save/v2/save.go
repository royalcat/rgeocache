package savev2

import (
	"encoding/binary"
	"fmt"
	"io"
	"iter"
	"math"
	"time"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1proto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	savev2proto "github.com/royalcat/rgeocache/cachesaver/save/v2/proto"
	"github.com/royalcat/rgeocache/kdbush"
	"google.golang.org/protobuf/proto"
)

const defaultNodeSize = kdbush.DefaultNodeSize

// rawEdge is a graph edge with its strings resolved, awaiting node-id to
// position translation.
type rawEdge struct {
	fromNode, toNode uint32
	street, name     string
	class, oneway    uint8
}

// Save writes a v2 cache to w.
//
// File layout:
//
//	[0..3]       "RGEO" magic
//	[4..7]       uint32 compat level = 2
//	[8..11]      uint32 v2header_size
//	[12..H]      V2Header protobuf
//	[H+..]       CacheMetadata protobuf
//	[..+I]       offset index: []uint32 (N unique strings × 4)
//	[..+D]       string data block (null-terminated concatenation)
//	[..+S]       ZonesSection protobuf (V2ZonesSection)
//	[..+Z]       KDBH binary block (points)
//	[..]         Graph section (optional; only written when edges are present)
//
// The graph section follows the point index and is invisible to readers that
// predate it. Its layout is documented in graph.go.
func Save(w io.Writer, items iter.Seq[cachemodel.Item], zones iter.Seq[cachemodel.Zone], meta cachemodel.Metadata) error {
	dedup := newStringsDedup()

	// Phase 1: Materialize points and edges with placeholder data.
	// Register strings to get IDs; we'll fill V2PointData after building the index.
	type rawPoint struct {
		x, y                                    float64
		name, street, houseNumber, city, region string
		weight                                  uint8
		geoType                                 uint8
		graphNode                               uint32
	}
	var rawPoints []rawPoint
	var rawEdges []rawEdge
	var maxGraphNode uint32
	for item := range items {
		if item.Kind == cachemodel.ItemEdge {
			e := item.Edge
			rawEdges = append(rawEdges, rawEdge{
				fromNode: e.FromNode,
				toNode:   e.ToNode,
				street:   e.Street.Value(),
				name:     e.Name.Value(),
				class:    e.Class,
				oneway:   e.Oneway,
			})
			// Edge strings must be registered before the string index is built.
			dedup.streets.Add(e.Street.Value())
			dedup.names.Add(e.Name.Value())
			continue
		}

		p := item.Point
		rawPoints = append(rawPoints, rawPoint{
			x: p.X, y: p.Y,
			name:        p.Data.Name.Value(),
			street:      p.Data.Street.Value(),
			houseNumber: p.Data.HouseNumber.Value(),
			city:        p.Data.City.Value(),
			region:      p.Data.Region.Value(),
			weight:      p.Data.Weight,
			geoType:     uint8(p.Data.Type),
			graphNode:   item.GraphNode,
		})
		if item.GraphNode > maxGraphNode {
			maxGraphNode = item.GraphNode
		}
		// Register strings to reserve IDs
		dedup.names.Add(p.Data.Name.Value())
		dedup.streets.Add(p.Data.Street.Value())
		dedup.houseNumbers.Add(p.Data.HouseNumber.Value())
		dedup.cities.Add(p.Data.City.Value())
		dedup.regions.Add(p.Data.Region.Value())
	}

	// Phase 2: Build offset index and null-terminated string data block
	offsetIndex, stringData := buildStringIndex(dedup)

	// Phase 3: Fill V2PointData using the assigned IDs. Also record the original
	// point index of every graph node (id → index) for edge translation.
	v2points := make([]kdbush.Point[V2PointData], len(rawPoints))
	// Graph node ids are dense (assigned by claimGraphNode) and every id has
	// exactly one point, so maxGraphNode+1 is the exact size of the id→point
	// index. Preallocate once: growing to graphNode+1 on every new maximum is
	// quadratic, and an amortized length that no longer equals the highest id
	// would weaken writeGraphSection's out-of-range node check.
	var nodeToOrig []uint32
	if maxGraphNode > 0 {
		nodeToOrig = make([]uint32, int(maxGraphNode)+1)
	}
	for i, rp := range rawPoints {
		v2points[i] = kdbush.Point[V2PointData]{
			X: rp.x, Y: rp.y,
			Data: V2PointData{
				NameID:        dedup.names.Add(rp.name),
				StreetID:      dedup.streets.Add(rp.street),
				HouseNumberID: dedup.houseNumbers.Add(rp.houseNumber),
				CityID:        dedup.cities.Add(rp.city),
				RegionID:      dedup.regions.Add(rp.region),
				Weight:        rp.weight,
				GeoType:       rp.geoType,
			},
		}
		if rp.graphNode != 0 {
			nodeToOrig[rp.graphNode] = uint32(i)
		}
	}
	rawPoints = nil // release to GC

	// Phase 4: Materialize zones with inline names
	zonesSection := buildZonesSection(zones)
	zonesBytes, err := proto.Marshal(zonesSection)
	if err != nil {
		return err
	}

	// Phase 5: Marshal metadata
	metadataProto := &savev1proto.CacheMetadata{
		Version:     meta.Version,
		DateCreated: meta.DateCreated.Format(time.RFC3339),
		Locale:      meta.Locale,
	}
	metadataBytes, err := proto.Marshal(metadataProto)
	if err != nil {
		return err
	}

	// Phase 6: V2Header
	header := &savev2proto.V2Header{
		MetadataSize:     uint32(len(metadataBytes)),
		StringsIndexSize: uint32(len(offsetIndex) * 4),
		StringsDataSize:  uint32(len(stringData)),
		ZonesSize:        uint32(len(zonesBytes)),
	}
	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return err
	}

	// Phase 7: Write everything sequentially
	if err := binary.Write(w, binary.LittleEndian, uint32(len(headerBytes))); err != nil {
		return err
	}
	if _, err := w.Write(headerBytes); err != nil {
		return err
	}
	if _, err := w.Write(metadataBytes); err != nil {
		return err
	}
	// Write offset index as raw uint32 array
	if err := binary.Write(w, binary.LittleEndian, offsetIndex); err != nil {
		return err
	}
	if _, err := w.Write(stringData); err != nil {
		return err
	}
	if _, err := w.Write(zonesBytes); err != nil {
		return err
	}

	// KDBH block. Keep the sorted index permutation to translate graph node ids
	// to point positions.
	_, sortedIdxs, err := kdbush.BuildDiskWithMapping[V2PointData, *V2PointData](v2points, defaultNodeSize, w)
	if err != nil {
		return err
	}

	if len(rawEdges) > 0 {
		origToPos := make([]uint32, len(sortedIdxs))
		for pos, orig := range sortedIdxs {
			origToPos[orig] = uint32(pos)
		}
		if err := writeGraphSection(w, v2points, rawEdges, nodeToOrig, origToPos, dedup); err != nil {
			return err
		}
	}

	return nil
}

// writeGraphSection appends the optional graph section after the point KDBH
// block. See graph.go for the layout; writeGraphIndexSize must match the KDBH
// writer's output exactly.
func writeGraphSection(
	w io.Writer,
	points []kdbush.Point[V2PointData],
	edges []rawEdge,
	nodeToOrig []uint32,
	origToPos []uint32,
	dedup *stringsDedup,
) error {
	prepared := make([]kdbush.Point[graphEdgeRecord], 0, len(edges))
	maxHalfExtent := 0.0

	for _, e := range edges {
		if int(e.fromNode) >= len(nodeToOrig) || int(e.toNode) >= len(nodeToOrig) {
			return fmt.Errorf(
				"savev2: graph edge %d -> %d references a node without an emitted point",
				e.fromNode, e.toNode,
			)
		}
		fromOrig := nodeToOrig[e.fromNode]
		toOrig := nodeToOrig[e.toNode]

		// points is still in original order: BuildDisk does not reorder the
		// source slice.
		ax, ay := points[fromOrig].X, points[fromOrig].Y
		bx, by := points[toOrig].X, points[toOrig].Y

		if halfDiag := 0.5 * math.Hypot(bx-ax, by-ay); halfDiag > maxHalfExtent {
			maxHalfExtent = halfDiag
		}

		prepared = append(prepared, kdbush.Point[graphEdgeRecord]{
			X: (ax + bx) / 2,
			Y: (ay + by) / 2,
			Data: graphEdgeRecord{
				FromPos: origToPos[fromOrig],
				ToPos:   origToPos[toOrig],
				// Add returns the already-assigned id: edge strings were
				// registered in phase 1, before the string index was built.
				StreetID: dedup.streets.Add(e.street),
				NameID:   dedup.names.Add(e.name),
				Class:    e.class,
				Oneway:   e.oneway,
			},
		})
	}

	edgeCount := uint64(len(prepared))
	indexSize := graphEdgeIndexSize(edgeCount)
	totalSize := int64(graphHeaderSize) + indexSize

	header := make([]byte, graphHeaderSize)
	copy(header[0:4], graphMagic)
	binary.LittleEndian.PutUint32(header[4:8], graphVersion)
	binary.LittleEndian.PutUint32(header[8:12], graphFlagHasDirections)
	binary.LittleEndian.PutUint64(header[12:20], edgeCount)
	binary.LittleEndian.PutUint64(header[20:28], math.Float64bits(maxHalfExtent))
	binary.LittleEndian.PutUint64(header[28:36], uint64(totalSize))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("savev2: writing graph header: %w", err)
	}

	written, err := kdbush.BuildDisk[graphEdgeRecord, *graphEdgeRecord](prepared, defaultNodeSize, w)
	if err != nil {
		return fmt.Errorf("savev2: writing graph edge index: %w", err)
	}
	if written != indexSize {
		return fmt.Errorf("savev2: graph edge index wrote %d bytes, expected %d", written, indexSize)
	}

	return nil
}

// buildZonesSection converts zones to V2ZonesSection proto with inline names and geometry.
func buildZonesSection(zones iter.Seq[cachemodel.Zone]) *savev2proto.V2ZonesSection {
	var regionZones []*savev2proto.V2Zone
	var countryZones []*savev2proto.V2Zone

	for z := range zones {
		v2z := &savev2proto.V2Zone{
			Name:         []byte(z.Name.Value()),
			Bounds:       mapBoundsToV2(z.Bounds),
			MultiPolygon: mapMultiPolygonToV2(z.Polygon),
		}
		switch z.Type {
		case cachemodel.ZoneRegion:
			regionZones = append(regionZones, v2z)
		case cachemodel.ZoneCountry:
			countryZones = append(countryZones, v2z)
		}
	}

	sec := &savev2proto.V2ZonesSection{}
	if len(regionZones) > 0 {
		sec.Blobs = append(sec.Blobs, &savev2proto.V2ZoneBlob{
			ZoneType: 1,
			Zones:    regionZones,
		})
	}
	if len(countryZones) > 0 {
		sec.Blobs = append(sec.Blobs, &savev2proto.V2ZoneBlob{
			ZoneType: 2,
			Zones:    countryZones,
		})
	}
	return sec
}
