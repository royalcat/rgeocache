package savev2

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev1proto "github.com/royalcat/rgeocache/cachesaver/save/v1/proto"
	savev2proto "github.com/royalcat/rgeocache/cachesaver/save/v2/proto"
	"github.com/royalcat/rgeocache/internal/spool"
	"github.com/royalcat/rgeocache/internal/strtab"
	"github.com/royalcat/rgeocache/kdbush"
	"golang.org/x/exp/mmap"
	"google.golang.org/protobuf/proto"
)

const invalidNode = math.MaxUint32

// WriteSpool assembles a v2 cache from the parse spool. The caller is
// responsible for writing the magic bytes and compatibility level.
//
// The writer keeps the cache payload on disk: only graph node ids, point
// coordinates for the current phase and the KDBH sort arrays live in RAM.
func WriteSpool(
	w io.Writer,
	sp *spool.Reader,
	table *strtab.Table,
	zones []cachemodel.Zone,
	meta cachemodel.Metadata,
	workDir string,
) error {
	bw := bufio.NewWriterSize(w, 1<<20)
	if err := writeSpoolBody(bw, sp, table, zones, meta, workDir); err != nil {
		return err
	}
	return bw.Flush()
}

func writeSpoolBody(
	w io.Writer,
	sp *spool.Reader,
	table *strtab.Table,
	zones []cachemodel.Zone,
	meta cachemodel.Metadata,
	workDir string,
) error {
	n := sp.NumPoints()
	if n > math.MaxUint32 {
		return fmt.Errorf("savev2: too many points: %d", n)
	}
	slog.Info("Writing v2 cache from spool", "points", n, "edges", sp.NumEdges())
	warnIfWriterTooLarge(n, sp.NumEdges())

	// 1. Canonicalize road node occurrences by OSM node key. The occurrence
	// with the lowest ClaimSeq wins; ClaimSeq is assigned from the way input
	// order, so the result is deterministic across runs.
	canonical, nodeCount, err := canonicalizePoints(sp, workDir)
	if err != nil {
		return err
	}

	// 2. Assign dense graph node ids to canonical road points and collect their
	// coordinates. The dense id of a canonical occurrence is its rank in the
	// canonical bitset, so no per-occurrence id table is kept.
	canon := newRankSet(n)
	coords := make([]graphPoint, 0, nodeCount)
	plainPoints := 0 // points with no OSM node key; all are kept
	if err := sp.IteratePoints(func(occ int, p spool.Point) error {
		if p.NodeKey == 0 {
			plainPoints++
			return nil
		}
		if canonical[occ] == uint32(occ) {
			canon.Set(uint32(occ))
			coords = append(coords, graphPoint{x: p.X, y: p.Y})
		}
		return nil
	}); err != nil {
		return err
	}
	canon.Finish()
	if len(coords) != nodeCount || canon.Count() != nodeCount {
		return fmt.Errorf("savev2: collected %d road node coordinates, expected %d", len(coords), nodeCount)
	}
	slog.Info("Canonicalized road nodes", "graph_nodes", nodeCount)

	// 3. Canonicalize edge endpoints, simplify the graph and collect the keep
	// bitmap. The simplified edges are spooled, never held in memory.
	var (
		simpleEdges *spool.Reader
		keepNode    []uint64
	)
	if sp.NumEdges() > 0 {
		simpleEdges, keepNode, err = simplifySpoolEdges(sp, canonical, canon, coords, nodeCount, workDir)
		if err != nil {
			return err
		}
		slog.Info("Simplified road graph", "edges", simpleEdges.NumEdges())
	} else {
		keepNode = make([]uint64, (nodeCount+63)/64)
		for i := range keepNode {
			keepNode[i] = ^uint64(0)
		}
		// Bits past nodeCount in the last word must not be counted below.
		if rem := nodeCount % 64; rem != 0 {
			keepNode[len(keepNode)-1] = 1<<rem - 1
		}
	}
	canonical = nil
	coords = nil
	debug.FreeOSMemory()

	// 4. Build the point index: filter dropped/duplicate road points and keep
	// their coordinates for the KDBH sort. The kept count is known up front
	// (every plain point plus every surviving road node), so the arrays are
	// allocated exactly.
	count := plainPoints
	for _, w := range keepNode {
		count += bits.OnesCount64(w)
	}
	if count > math.MaxUint32 {
		return fmt.Errorf("savev2: too many points after simplification: %d", count)
	}
	sortIdxs := make([]uint32, 0, count)
	sortCoords := make([]float64, 0, 2*count)
	nodeFiltered := make([]uint32, nodeCount) // later reused as node -> KDBH position
	filtered := 0
	if err := sp.IteratePoints(func(occ int, p spool.Point) error {
		if !pointKept(p, occ, canon, keepNode) {
			return nil
		}
		if p.NodeKey != 0 {
			nodeFiltered[canon.Rank(uint32(occ))] = uint32(filtered)
		}
		sortIdxs = append(sortIdxs, uint32(filtered))
		sortCoords = append(sortCoords, p.X, p.Y)
		filtered++
		return nil
	}); err != nil {
		return err
	}
	if filtered != count {
		return fmt.Errorf("savev2: kept %d points, expected %d", filtered, count)
	}
	slog.Info("Built point filter", "points", count)
	kdbush.Sort32(sortIdxs, sortCoords, defaultNodeSize)

	// 5. Header, metadata, string table and zones.
	offsetIndex, stringDataSize, err := table.Index()
	if err != nil {
		return err
	}

	zonesBytes, err := proto.Marshal(buildZonesSection(zones))
	if err != nil {
		return err
	}
	metadataProto := &savev1proto.CacheMetadata{
		Version:     meta.Version,
		DateCreated: meta.DateCreated.UTC().Format(time.RFC3339),
		Locale:      meta.Locale,
	}
	metadataBytes, err := proto.Marshal(metadataProto)
	if err != nil {
		return err
	}

	header := &savev2proto.V2Header{
		MetadataSize:     uint32(len(metadataBytes)),
		StringsIndexSize: uint32(len(offsetIndex) * 4),
		StringsDataSize:  uint32(stringDataSize),
		ZonesSize:        uint32(len(zonesBytes)),
	}
	headerBytes, err := proto.Marshal(header)
	if err != nil {
		return err
	}

	if err := binary.Write(w, binary.LittleEndian, uint32(len(headerBytes))); err != nil {
		return err
	}
	if _, err := w.Write(headerBytes); err != nil {
		return err
	}
	if _, err := w.Write(metadataBytes); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, offsetIndex); err != nil {
		return err
	}
	offsetIndex = nil
	if err := table.WriteData(w); err != nil {
		return err
	}
	if _, err := w.Write(zonesBytes); err != nil {
		return err
	}

	// 6. Point KDBH: index in tree order, data blobs in filtered order.
	if _, err := kdbush.WriteIndex32(w, sortIdxs, sortCoords, defaultNodeSize); err != nil {
		return err
	}
	if _, err := kdbush.WriteFixedOffsets(w, count, v2PointDataSize); err != nil {
		return err
	}
	written := 0
	var rec [v2PointDataSize]byte
	if err := sp.IteratePoints(func(occ int, p spool.Point) error {
		if !pointKept(p, occ, canon, keepNode) {
			return nil
		}
		encodeSpoolPoint(rec[:], p)
		if _, err := w.Write(rec[:]); err != nil {
			return err
		}
		written++
		return nil
	}); err != nil {
		return err
	}
	if written != count {
		return fmt.Errorf("savev2: wrote %d point records, expected %d", written, count)
	}

	// The graph needs each node's KDBH position. sortIdxs maps position ->
	// filtered index; inverting it in place gives filtered index -> position,
	// and nodeFiltered is then rewritten in place to node -> position.
	invertPermutation(sortIdxs)
	for dense, f := range nodeFiltered {
		nodeFiltered[dense] = sortIdxs[f]
	}
	nodePos := nodeFiltered

	// The graph only needs the tree-order coordinates, node positions and the
	// simplified edge spool.
	nodeFiltered = nil
	sortIdxs = nil
	canon = nil
	keepNode = nil
	debug.FreeOSMemory()

	if simpleEdges != nil && simpleEdges.NumEdges() > 0 {
		if err := writeSpoolGraph(w, simpleEdges, sortCoords, nodePos); err != nil {
			return err
		}
		slog.Info("Wrote road graph", "edges", simpleEdges.NumEdges())
	}

	return nil
}

// canonicalizePoints returns, for every road point occurrence, the index of the
// first occurrence of the same OSM node.
func canonicalizePoints(sp *spool.Reader, workDir string) ([]uint32, int, error) {
	n := sp.NumPoints()
	canonical := make([]uint32, n)

	sorter, err := spool.NewPairSorter(spool.SortDir(workDir), spool.DefaultSortPairs)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = sorter.Close() }()

	if err := sp.IteratePoints(func(occ int, p spool.Point) error {
		if p.NodeKey == 0 {
			return nil
		}
		canonical[occ] = uint32(occ)
		// Prefer a way that carries a street or name over one that does not.
		// The high bit sorts empty occurrences after named ones; the low bits
		// keep the deterministic way input order within each group.
		priority := uint32(0)
		if p.StreetID == 0 && p.NameID == 0 {
			priority = 1 << 31
		}
		return sorter.Add(p.NodeKey, priority|p.ClaimSeq, uint32(occ))
	}); err != nil {
		return nil, 0, err
	}

	var (
		haveKey bool
		curKey  uint64
		first   uint32
		groups  int
	)
	if err := sorter.Iterate(func(p spool.Pair) error {
		if !haveKey || p.Key != curKey {
			haveKey = true
			curKey = p.Key
			first = p.Occ
			groups++
		}
		canonical[p.Occ] = first
		return nil
	}); err != nil {
		return nil, 0, err
	}

	return canonical, groups, nil
}

// simplifySpoolEdges remaps edge occurrences to dense graph ids, simplifies the
// graph and returns the simplified edge spool plus the keep bitmap.
func simplifySpoolEdges(
	sp *spool.Reader,
	canonical []uint32,
	canon *rankSet,
	coords []graphPoint,
	nodeCount int,
	workDir string,
) (*spool.Reader, []uint64, error) {
	denseDir := filepath.Join(workDir, "edges-dense")
	dw, err := spool.Create(denseDir)
	if err != nil {
		return nil, nil, err
	}
	if err := sp.IterateEdges(func(i int, e spool.Edge) error {
		cf, ct := canonical[e.From], canonical[e.To]
		if !canon.Has(cf) || !canon.Has(ct) {
			return fmt.Errorf("savev2: edge %d references a non-canonical road point", i)
		}
		from, to := canon.Rank(cf), canon.Rank(ct)
		return dw.WriteEdge(spool.Edge{
			From:     from,
			To:       to,
			StreetID: e.StreetID,
			NameID:   e.NameID,
			Class:    e.Class,
			Oneway:   e.Oneway,
		})
	}); err != nil {
		_ = dw.Close()
		return nil, nil, err
	}
	if err := dw.Close(); err != nil {
		return nil, nil, err
	}

	denseReader, err := spool.Open(denseDir)
	if err != nil {
		return nil, nil, err
	}
	src, err := newSpoolEdgeSource(denseReader)
	if err != nil {
		return nil, nil, err
	}

	simpleDir := filepath.Join(workDir, "edges-simple")
	sw, err := spool.Create(simpleDir)
	if err != nil {
		_ = src.Close()
		return nil, nil, err
	}
	keep, err := simplifyGraphCore(src, coords, nodeCount, func(e graphEdge) error {
		return sw.WriteEdge(spool.Edge{
			From:     e.from,
			To:       e.to,
			StreetID: e.streetID,
			NameID:   e.nameID,
			Class:    e.class,
			Oneway:   e.oneway,
		})
	})
	_ = src.Close()
	if cerr := sw.Close(); err == nil {
		err = cerr
	}
	_ = os.RemoveAll(denseDir)
	if err != nil {
		return nil, nil, err
	}

	simpleReader, err := spool.Open(simpleDir)
	if err != nil {
		return nil, nil, err
	}
	return simpleReader, keep, nil
}

func pointKept(p spool.Point, occ int, canon *rankSet, keepNode []uint64) bool {
	if p.NodeKey == 0 {
		return true
	}
	if !canon.Has(uint32(occ)) {
		return false
	}
	dense := canon.Rank(uint32(occ))
	return keepNode[dense>>6]&(1<<(dense&63)) != 0
}

func encodeSpoolPoint(dst []byte, p spool.Point) {
	binary.LittleEndian.PutUint32(dst[0:4], p.NameID)
	binary.LittleEndian.PutUint32(dst[4:8], p.StreetID)
	binary.LittleEndian.PutUint32(dst[8:12], p.HouseNumberID)
	binary.LittleEndian.PutUint32(dst[12:16], p.CityID)
	binary.LittleEndian.PutUint32(dst[16:20], p.RegionID)
	dst[20] = p.Weight
	dst[21] = p.GeoType
}

// writeSpoolGraph appends the optional graph section. pointCoords are the point
// coordinates in KDBH order; nodePos maps graph node ids to KDBH positions.
func writeSpoolGraph(w io.Writer, edges *spool.Reader, pointCoords []float64, nodePos []uint32) error {
	m := edges.NumEdges()
	if m == 0 {
		return nil
	}

	ecoords := make([]float64, 0, 2*m)
	if uint64(m) > math.MaxUint32 {
		return fmt.Errorf("savev2: too many graph edges: %d", m)
	}
	eidxs := make([]uint32, 0, m)
	maxHalfExtent := 0.0
	if err := edges.IterateEdges(func(i int, e spool.Edge) error {
		ax, ay := pointCoords[2*nodePos[e.From]], pointCoords[2*nodePos[e.From]+1]
		bx, by := pointCoords[2*nodePos[e.To]], pointCoords[2*nodePos[e.To]+1]
		ecoords = append(ecoords, (ax+bx)/2, (ay+by)/2)
		eidxs = append(eidxs, uint32(i))
		if h := 0.5 * math.Hypot(bx-ax, by-ay); h > maxHalfExtent {
			maxHalfExtent = h
		}
		return nil
	}); err != nil {
		return err
	}
	kdbush.Sort32(eidxs, ecoords, defaultNodeSize)

	indexSize := graphEdgeIndexSize(uint64(m)) // full KDBH block: header + tree + offsets + records
	treeSize := int64(32 + int64(m)*8 + int64(m)*16)
	totalSize := int64(graphHeaderSize) + indexSize
	header := make([]byte, graphHeaderSize)
	copy(header[0:4], graphMagic)
	binary.LittleEndian.PutUint32(header[4:8], graphVersion)
	binary.LittleEndian.PutUint32(header[8:12], graphFlagHasDirections)
	binary.LittleEndian.PutUint64(header[12:20], uint64(m))
	binary.LittleEndian.PutUint64(header[20:28], math.Float64bits(maxHalfExtent))
	binary.LittleEndian.PutUint64(header[28:36], uint64(totalSize))
	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("savev2: writing graph header: %w", err)
	}

	written, err := kdbush.WriteIndex32(w, eidxs, ecoords, defaultNodeSize)
	if err != nil {
		return fmt.Errorf("savev2: writing graph edge index: %w", err)
	}
	if written != treeSize {
		return fmt.Errorf("savev2: graph edge index wrote %d bytes, expected %d", written, treeSize)
	}
	if _, err := kdbush.WriteFixedOffsets(w, m, graphEdgeRecordSize); err != nil {
		return err
	}

	var rec [graphEdgeRecordSize]byte
	writtenEdges := 0
	if err := edges.IterateEdges(func(i int, e spool.Edge) error {
		binary.LittleEndian.PutUint32(rec[0:4], nodePos[e.From])
		binary.LittleEndian.PutUint32(rec[4:8], nodePos[e.To])
		binary.LittleEndian.PutUint32(rec[8:12], e.StreetID)
		binary.LittleEndian.PutUint32(rec[12:16], e.NameID)
		rec[16] = e.Class
		rec[17] = e.Oneway
		if _, err := w.Write(rec[:]); err != nil {
			return err
		}
		writtenEdges++
		return nil
	}); err != nil {
		return err
	}
	if writtenEdges != m {
		return fmt.Errorf("savev2: wrote %d graph edges, expected %d", writtenEdges, m)
	}
	return nil
}

// spoolEdgeSource adapts an mmap'd edge spool to the simplifier's edgeSource.
type spoolEdgeSource struct {
	r  *spool.Reader
	ra *mmap.ReaderAt
}

func newSpoolEdgeSource(r *spool.Reader) (*spoolEdgeSource, error) {
	ra, err := r.MmapEdges()
	if err != nil {
		return nil, err
	}
	return &spoolEdgeSource{r: r, ra: ra}, nil
}

func (s *spoolEdgeSource) Len() int { return s.r.NumEdges() }

func (s *spoolEdgeSource) At(i int) (graphEdge, error) {
	e, err := spool.ReadEdgeAt(s.ra, i)
	if err != nil {
		return graphEdge{}, err
	}
	return graphEdge{
		from:     e.From,
		to:       e.To,
		streetID: e.StreetID,
		nameID:   e.NameID,
		class:    e.Class,
		oneway:   e.Oneway,
	}, nil
}

func (s *spoolEdgeSource) Close() error { return s.ra.Close() }

// writerPeakBytes upper-bounds the writer's live heap for n point occurrences
// and e spooled edges, taking every road node and every point as distinct
// (real data is ~15% below that). Phases, in the order they run:
//
//	canonicalize  canonical 4n + sort buffer
//	graph         canonical 4n + rank 0.14n + coords 16v + simplifier 16v + 8e
//	point index   rank 0.14n + sortIdxs 4p + sortCoords 16p + nodePos 4v
//
// so the peak is the larger of the graph and point-index phases.
func writerPeakBytes(n, e int) uint64 {
	const sortBuffer = 16 * 1024 * 1024 * 16 // spool.DefaultSortPairs * 16 B
	rank := uint64(n) * 15 / 100
	graph := 4*uint64(n) + rank + 32*uint64(n) + 8*uint64(e)
	index := rank + 24*uint64(n) + 4*uint64(n)
	return max(graph, index, 4*uint64(n)+sortBuffer)
}

// warnIfWriterTooLarge logs the projected peak against the Go memory limit so
// an undersized machine is flagged before the writer is OOM-killed.
func warnIfWriterTooLarge(n, e int) {
	peak := writerPeakBytes(n, e)
	limit := debug.SetMemoryLimit(-1)
	if limit > 0 && limit < math.MaxInt64 && peak > uint64(limit) {
		slog.Warn("Writer may exceed the memory limit; split the input into smaller regions",
			"projected_peak_mib", peak>>20, "memory_limit_mib", uint64(limit)>>20)
		return
	}
	slog.Info("Writer memory projection", "projected_peak_mib", peak>>20)
}
