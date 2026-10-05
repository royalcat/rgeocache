// Package fgeocode implements forward (text) geocoding over a Bleve index
// built from the same cache the reverse geocoder reads.
//
// The API it backs is feature-compatible with the Rust server's
// /fgeocode/search and /fgeocode/autocomplete endpoints: the same parameters,
// response shape and "503 while the index builds" behaviour. The matching and
// ranking are deliberately a simpler Bleve implementation, not a port of the
// tantivy query builder.
package fgeocode

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/geocoder"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const (
	// MaxQueryLen is the maximum length of the free-text query and of each
	// structured field, mirroring the Rust server.
	MaxQueryLen = 256
	// DefaultLimit is the result count used when the request does not set one.
	DefaultLimit = 10
	// MinLimit and MaxLimit bound the requested result count.
	MinLimit = 1
	MaxLimit = 100

	// Result over-fetching: a road is indexed once per resampled point and an
	// area once per Poisson-filled point, so duplicates are collapsed after a
	// larger fetch. The same bounds as the Rust server keep pagination shallow.
	overFetchFactor   = 10
	minOverFetch      = 50
	maxOverFetch      = 500
	batchSize         = 5000
	progressEvery     = 1_000_000
	maxSuggestionScan = 10_000
)

// Object kinds as they appear in responses (`geo_type`) and in the `kind`
// filter of a search request.
const (
	KindZone     = "zone"
	KindBuilding = "building"
	KindRoad     = "road"
	KindArea     = "area"
)

// ErrBuilding is returned by Search and Suggest while the index is still
// being built in the background.
var ErrBuilding = errors.New("forward geocoder index is still building")

// Compile-time checks that both loaded geocoders can feed a forward index.
var (
	_ Source = (*geocoder.RGeoCoder)(nil)
	_ Source = (*geocoder.RGeoCoderDisk)(nil)
)

// Source is the slice of a loaded geocoder the forward geocoder needs.
// Both *geocoder.RGeoCoder and *geocoder.RGeoCoderDisk implement it.
type Source interface {
	// ForEachPoint calls fn for every cached point (X is longitude, Y is
	// latitude) until fn returns false.
	ForEachPoint(fn func(cachemodel.Point) bool)
	// Zones returns the cache's region and country zones.
	Zones() []cachemodel.Zone
	// NumPoints returns the number of cached points.
	NumPoints() int
	// Metadata returns the cache metadata (zero value when absent).
	Metadata() cachemodel.Metadata
	// CountryAt returns the country containing the given coordinates.
	CountryAt(lat, lon float64) (string, bool)
}

// Config configures a forward geocoder.
type Config struct {
	Source Source
	// IndexDir is the directory used to persist the index. When empty a
	// temporary directory is used and removed on Close.
	IndexDir string
	// CacheFile is the cache file the index is built from, used for the
	// fingerprint that guards index reuse. Optional.
	CacheFile string
	Logger    *slog.Logger
}

// IndexMode reports how the current index came to be.
type IndexMode int

const (
	IndexBuilt IndexMode = iota
	IndexReused
)

type buildState struct {
	index bleve.Index
	err   error
}

// Geocoder is a forward geocoder backed by a Bleve index. Build it with New
// and run Build (usually in a goroutine); Search and Suggest answer
// ErrBuilding until the build finishes.
type Geocoder struct {
	source  Source
	cfg     Config
	log     *slog.Logger
	zones   []cachemodel.Zone
	tempDir string

	state  atomic.Pointer[buildState]
	reused atomic.Bool

	closeOnce sync.Once
	closeErr  error
}

// New creates a forward geocoder for the given source. No index is built
// until Build is called.
func New(cfg Config) *Geocoder {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Geocoder{
		source: cfg.Source,
		cfg:    cfg,
		log:    log,
		zones:  cfg.Source.Zones(),
	}
}

// Build opens a persisted index or builds a new one. It stores the result, so
// Search/Suggest start answering as soon as it returns; on failure the error
// is stored and returned from Ready forever after.
func (g *Geocoder) Build(ctx context.Context) {
	meter := otel.Meter("github.com/royalcat/rgeocache/fgeocode")
	built, _ := meter.Int64Counter("fgeocode_index_built_total",
		metric.WithDescription("Forward geocoder index builds"))
	reused, _ := meter.Int64Counter("fgeocode_index_reused_total",
		metric.WithDescription("Forward geocoder index reuses"))
	failures, _ := meter.Int64Counter("fgeocode_index_failures_total",
		metric.WithDescription("Forward geocoder index build failures"))
	buildDuration, _ := meter.Float64Histogram("fgeocode_index_build_duration_seconds",
		metric.WithDescription("Time spent building the forward geocoder index"),
		metric.WithUnit("s"))

	started := time.Now()
	index, mode, err := g.openOrBuild(ctx)
	buildDuration.Record(ctx, time.Since(started).Seconds())
	if err != nil {
		failures.Add(ctx, 1)
		g.log.Error("forward geocoder index build failed", "error", err)
		g.state.Store(&buildState{err: err})
		return
	}

	g.reused.Store(mode == IndexReused)
	if mode == IndexReused {
		reused.Add(ctx, 1)
	} else {
		built.Add(ctx, 1)
	}
	g.state.Store(&buildState{index: index})
}

// Ready reports whether the index is searchable. It returns ErrBuilding while
// the build runs and the build error if it failed.
func (g *Geocoder) Ready() error {
	_, err := g.ready()
	return err
}

// Reused reports whether the current index was loaded from disk rather than
// built during this run.
func (g *Geocoder) Reused() bool {
	return g.reused.Load()
}

// Close releases the index and removes the temporary directory, if any.
// It is safe to call multiple times.
func (g *Geocoder) Close() error {
	g.closeOnce.Do(func() {
		if st := g.state.Load(); st != nil && st.index != nil {
			g.closeErr = st.index.Close()
		}
		if g.tempDir != "" {
			if err := os.RemoveAll(g.tempDir); err != nil && g.closeErr == nil {
				g.closeErr = err
			}
		}
	})
	return g.closeErr
}

func (g *Geocoder) ready() (bleve.Index, error) {
	st := g.state.Load()
	if st == nil {
		return nil, ErrBuilding
	}
	if st.err != nil {
		return nil, st.err
	}
	return st.index, nil
}

func (g *Geocoder) openOrBuild(ctx context.Context) (bleve.Index, IndexMode, error) {
	fp := g.fingerprint()
	dir := g.cfg.IndexDir

	if dir != "" {
		if err := prepareIndexDir(dir); err != nil {
			return nil, IndexBuilt, err
		}
		if hasIndexMeta(dir) {
			reusable := false
			stored, readErr := readFingerprint(dir)
			switch {
			case readErr != nil:
				g.log.Warn("forward geocoder index has no usable fingerprint, rebuilding", "dir", dir, "error", readErr)
			case stored != fp:
				g.log.Info("forward geocoder index was built from a different cache, rebuilding", "dir", dir)
			default:
				reusable = true
			}
			if reusable {
				index, err := bleve.Open(dir)
				if err == nil {
					g.log.Info("reusing forward geocoder index",
						"dir", dir,
						"points", g.source.NumPoints(),
						"zones", len(g.zones),
					)
					return index, IndexReused, nil
				}
				g.log.Warn("failed to open persisted forward geocoder index, rebuilding", "dir", dir, "error", err)
			}
			if err := wipeDir(dir); err != nil {
				return nil, IndexBuilt, err
			}
		}
	} else {
		tempDir, err := os.MkdirTemp("", "rgeocache-fgeocode-*")
		if err != nil {
			return nil, IndexBuilt, fmt.Errorf("create forward geocoder temp dir: %w", err)
		}
		g.tempDir = tempDir
		dir = tempDir
	}

	mapping, err := buildMapping(g.source.Metadata().Locale)
	if err != nil {
		return nil, IndexBuilt, err
	}
	index, err := bleve.New(dir, mapping)
	if err != nil {
		return nil, IndexBuilt, fmt.Errorf("create forward geocoder index: %w", err)
	}
	if err := g.indexAll(ctx, index); err != nil {
		_ = index.Close()
		return nil, IndexBuilt, err
	}
	if g.cfg.IndexDir != "" {
		if err := writeFingerprint(g.cfg.IndexDir, fp); err != nil {
			g.log.Warn("failed to write forward geocoder index fingerprint", "error", err)
		}
	}
	return index, IndexBuilt, nil
}

// prepareIndexDir creates the directory when missing, accepts an empty one,
// and refuses to touch a non-empty directory that holds no Bleve index.
func prepareIndexDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	if !hasIndexMeta(dir) {
		return fmt.Errorf("forward geocoder index dir %q is non-empty but contains no index; refusing to overwrite it", dir)
	}
	return nil
}

const indexMetaFile = "index_meta.json"

func hasIndexMeta(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, indexMetaFile))
	return err == nil
}

// wipeDir removes the directory contents but keeps the directory itself.
func wipeDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// indexAll writes one document per point and one per zone, in batches. Scorch
// introduces each batch as a segment and merges them in the background while
// the next batches are indexed, so this is both faster to first answer and
// faster overall than the synchronous offline builder.
func (g *Geocoder) indexAll(ctx context.Context, index bleve.Index) error {
	started := time.Now()
	total := g.source.NumPoints() + len(g.zones)
	g.log.Info("building forward geocoder index", "points", g.source.NumPoints(), "zones", len(g.zones))

	batch := index.NewBatch()
	indexed := 0
	var stopErr error

	flush := func() error {
		if batch.Size() == 0 {
			return nil
		}
		err := index.Batch(batch)
		batch.Reset()
		return err
	}

	g.source.ForEachPoint(func(p cachemodel.Point) bool {
		if ctx.Err() != nil {
			stopErr = ctx.Err()
			return false
		}
		if err := batch.Index("p"+strconv.Itoa(indexed), g.pointDoc(p)); err != nil {
			stopErr = err
			return false
		}
		indexed++
		if indexed%progressEvery == 0 {
			if err := flush(); err != nil {
				stopErr = err
				return false
			}
			g.log.Info("forward geocoder indexing",
				"indexed", indexed, "total", total, "elapsed", time.Since(started).Round(time.Second))
			return true
		}
		if batch.Size() >= batchSize {
			if err := flush(); err != nil {
				stopErr = err
				return false
			}
		}
		return true
	})
	if stopErr != nil {
		return stopErr
	}

	for i, zone := range g.zones {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := batch.Index("z"+strconv.Itoa(i), zoneDoc(i, zone)); err != nil {
			return err
		}
	}
	if err := flush(); err != nil {
		return err
	}

	g.log.Info("forward geocoder index built", "documents", indexed+len(g.zones), "elapsed", time.Since(started).Round(time.Second))
	return nil
}

// doc is the indexed representation of one point or zone. Text fields are
// analyzed; display and the coordinates are stored so responses can be
// materialized from the hit alone.
type doc struct {
	Street    string  `json:"street"`
	House     string  `json:"house"`
	HouseNorm string  `json:"house_normalized"`
	City      string  `json:"city"`
	Region    string  `json:"region"`
	Name      string  `json:"name"`
	Country   string  `json:"country"`
	Address   string  `json:"address"`
	Suggest   string  `json:"suggest"`
	Kind      string  `json:"kind"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	ZoneIdx   float64 `json:"zone_idx"`
	Display   string  `json:"display"`
}

func (g *Geocoder) pointDoc(p cachemodel.Point) doc {
	info := p.Data
	house := info.HouseNumber.Value()
	country := ""
	if c, ok := g.source.CountryAt(p.Y, p.X); ok {
		country = c
	}
	city := info.City.Value()
	street := info.Street.Value()
	region := info.Region.Value()
	name := info.Name.Value()

	return doc{
		Street:    street,
		House:     house,
		HouseNorm: canonicalHouse(house),
		City:      city,
		Region:    region,
		Name:      name,
		Country:   country,
		Address:   mergeParts(country, city, street, house, name),
		Suggest:   mergeParts(country, region, city, street, name),
		Kind:      kindOf(info.Type, info.Weight),
		Lat:       p.Y,
		Lon:       p.X,
		ZoneIdx:   -1,
		Display:   mergeParts(region, city, street, house, name),
	}
}

func zoneDoc(i int, zone cachemodel.Zone) doc {
	lat, lon := zoneCentroid(zone)
	name := zone.Name.Value()
	return doc{
		Name:    name,
		Address: name,
		Suggest: name,
		Kind:    KindZone,
		Lat:     lat,
		Lon:     lon,
		ZoneIdx: float64(i),
		Display: name,
	}
}

func zoneCentroid(zone cachemodel.Zone) (lat, lon float64) {
	if !zone.Bounds.IsEmpty() {
		center := zone.Bounds.Center()
		return center[1], center[0]
	}
	var sumLon, sumLat float64
	var n int
	for _, polygon := range zone.Polygon {
		for _, ring := range polygon {
			for _, point := range ring {
				sumLon += point[0]
				sumLat += point[1]
				n++
			}
		}
	}
	if n == 0 {
		return 0, 0
	}
	return sumLat / float64(n), sumLon / float64(n)
}

// kindOf mirrors the Rust server: the explicit cache type wins, and legacy
// records without one fall back to the weight proxy (5 = road, 3/2 = area,
// anything else = building).
func kindOf(objectType cachemodel.GeoObjectType, weight uint8) string {
	switch objectType {
	case cachemodel.GeoObjectBuilding:
		return KindBuilding
	case cachemodel.GeoObjectRoad:
		return KindRoad
	case cachemodel.GeoObjectArea:
		return KindArea
	}
	switch weight {
	case 5:
		return KindRoad
	case 2, 3:
		return KindArea
	default:
		return KindBuilding
	}
}

// mergeParts joins non-empty, trimmed parts in render order.
func mergeParts(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, ", ")
}

// houseAliases expands Russian house-number type words the same way the Rust
// server does, so `12 корпус 1`, `12 к 1` and `12к1` meet at `12к1`.
var houseAliases = map[string]string{
	"к": "к", "корп": "к", "корпус": "к",
	"с": "с", "стр": "с", "строение": "с",
	"л": "л", "лит": "л", "литер": "л", "литера": "л",
}

// canonicalHouse lowercases and strips whitespace from a house number after
// expanding type-word aliases. Other separators are preserved (`12-1` stays
// `12-1`), because the tokenizer already splits on them.
func canonicalHouse(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, word := range strings.Fields(raw) {
		if canonical, ok := houseAliases[word]; ok {
			b.WriteString(canonical)
		} else {
			b.WriteString(word)
		}
	}
	return b.String()
}

// KindFilter selects which object kinds a search returns. Its zero value and
// a fully-enabled value both mean "all kinds".
type KindFilter struct {
	Zone     bool
	Building bool
	Road     bool
	Area     bool
}

// ParseKindFilter parses a comma-separated kind list (zone/building/road/area,
// aliases z/b/r/a). An empty or fully-unknown list means all kinds.
func ParseKindFilter(raw string) KindFilter {
	var k KindFilter
	for _, part := range strings.Split(raw, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "zone", "z":
			k.Zone = true
		case "building", "b":
			k.Building = true
		case "road", "r":
			k.Road = true
		case "area", "a":
			k.Area = true
		}
	}
	if k == (KindFilter{}) {
		return KindFilter{Zone: true, Building: true, Road: true, Area: true}
	}
	return k
}

func (k KindFilter) all() bool {
	return k == (KindFilter{}) || (k.Zone && k.Building && k.Road && k.Area)
}

func (k KindFilter) names() []string {
	var names []string
	if k.Zone {
		names = append(names, KindZone)
	}
	if k.Building {
		names = append(names, KindBuilding)
	}
	if k.Road {
		names = append(names, KindRoad)
	}
	if k.Area {
		names = append(names, KindArea)
	}
	return names
}

// SearchRequest is one forward geocoding query.
type SearchRequest struct {
	// Query is free text matched against the merged address field.
	Query string
	// Structured fields are each matched against their own field and are
	// AND-ed together and with Query.
	City   string
	Region string
	Street string
	House  string
	Name   string
	// Kinds restricts the object kinds returned; the zero value means all.
	Kinds KindFilter
	// Limit is clamped to [MinLimit, MaxLimit]; zero means DefaultLimit.
	Limit int
	// Offset skips that many collapsed results. Pagination is shallow,
	// bounded by the over-fetch.
	Offset int
	// IncludePolygon includes the full multipolygon for zone hits.
	IncludePolygon bool
}

// Result is one forward geocoding hit.
type Result struct {
	AddressString string           `json:"address_string"`
	Score         float64          `json:"score"`
	Point         [2]float64       `json:"point"` // [lat, lon]
	GeoType       string           `json:"geo_type"`
	Country       string           `json:"country"`
	Multipolygon  orb.MultiPolygon `json:"multipolygon,omitempty"`
}

// SearchResponse is the /fgeocode/search response body.
type SearchResponse struct {
	Results []Result `json:"results"`
}

// Suggestion is one autocomplete hit.
type Suggestion struct {
	Text    string `json:"text"`
	DocFreq uint64 `json:"doc_freq"`
}

// AutocompleteResponse is the /fgeocode/autocomplete response body.
type AutocompleteResponse struct {
	Suggestions []Suggestion `json:"suggestions"`
}

// Search runs a forward geocoding query. It returns ErrBuilding while the
// index is still being built.
func (g *Geocoder) Search(req SearchRequest) ([]Result, error) {
	index, err := g.ready()
	if err != nil {
		return nil, err
	}

	limit := clampLimit(req.Limit)
	offset := max(req.Offset, 0)

	fetch := (offset + limit) * overFetchFactor
	fetch = min(max(fetch, minOverFetch), maxOverFetch)

	searchRequest := bleve.NewSearchRequestOptions(buildQuery(req), fetch, 0, false)
	searchRequest.Fields = []string{fieldDisplay, fieldLat, fieldLon, fieldKind, fieldZoneIdx}
	searchResult, err := index.Search(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("forward search: %w", err)
	}

	results := make([]Result, 0, min(fetch, limit))
	seen := make(map[string]struct{}, len(searchResult.Hits))
	for _, hit := range searchResult.Hits {
		item, key, ok := g.materialize(hit, req.IncludePolygon)
		if !ok {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		results = append(results, item)
	}

	if offset >= len(results) {
		return []Result{}, nil
	}
	results = results[offset:]
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// Suggest returns distinct `suggest` terms starting with prefix, ranked by
// document frequency. It returns ErrBuilding while the index is still being
// built.
func (g *Geocoder) Suggest(prefix string, limit int) ([]Suggestion, error) {
	index, err := g.ready()
	if err != nil {
		return nil, err
	}

	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return []Suggestion{}, nil
	}
	limit = clampLimit(limit)

	dictionary, err := index.FieldDictPrefix(fieldSuggest, []byte(prefix))
	if err != nil {
		return nil, fmt.Errorf("forward suggestion: %w", err)
	}
	defer func() { _ = dictionary.Close() }()

	type entry struct {
		text  string
		count uint64
	}
	entries := make([]entry, 0, limit)
	for scanned := 0; scanned < maxSuggestionScan; scanned++ {
		dictEntry, err := dictionary.Next()
		if err != nil {
			return nil, fmt.Errorf("forward suggestion: %w", err)
		}
		if dictEntry == nil {
			break
		}
		entries = append(entries, entry{text: dictEntry.Term, count: dictEntry.Count})
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].text < entries[j].text
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}

	suggestions := make([]Suggestion, len(entries))
	for i, e := range entries {
		suggestions[i] = Suggestion{Text: e.text, DocFreq: e.count}
	}
	return suggestions, nil
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	return min(max(limit, MinLimit), MaxLimit)
}

// buildQuery AND-s the free-text query, every structured field and the kind
// filter. An empty request matches everything.
func buildQuery(req SearchRequest) query.Query {
	var clauses []query.Query

	if q := strings.TrimSpace(req.Query); q != "" {
		clauses = append(clauses, matchField(fieldAddress, q))
	}

	for _, field := range []struct{ name, value string }{
		{"city", req.City},
		{"region", req.Region},
		{"street", req.Street},
		{"name", req.Name},
	} {
		if value := strings.TrimSpace(field.value); value != "" {
			clauses = append(clauses, matchField(field.name, value))
		}
	}

	if house := strings.TrimSpace(req.House); house != "" {
		houseClauses := []query.Query{matchField("house", house)}
		if normalized := canonicalHouse(house); normalized != "" && normalized != house {
			term := bleve.NewTermQuery(normalized)
			term.SetField(fieldHouseNormalized)
			houseClauses = append(houseClauses, term)
		}
		if len(houseClauses) == 1 {
			clauses = append(clauses, houseClauses[0])
		} else {
			clauses = append(clauses, bleve.NewDisjunctionQuery(houseClauses...))
		}
	}

	if !req.Kinds.all() {
		kindQueries := make([]query.Query, 0, 4)
		for _, kind := range req.Kinds.names() {
			term := bleve.NewTermQuery(kind)
			term.SetField(fieldKind)
			kindQueries = append(kindQueries, term)
		}
		switch len(kindQueries) {
		case 0:
			return bleve.NewMatchNoneQuery()
		case 1:
			clauses = append(clauses, kindQueries[0])
		default:
			clauses = append(clauses, bleve.NewDisjunctionQuery(kindQueries...))
		}
	}

	switch len(clauses) {
	case 0:
		return bleve.NewMatchAllQuery()
	case 1:
		return clauses[0]
	default:
		return bleve.NewConjunctionQuery(clauses...)
	}
}

// matchField builds an AND MatchQuery against one field, so every token of the
// value must match in that field.
func matchField(field, value string) query.Query {
	match := bleve.NewMatchQuery(value)
	match.SetField(field)
	match.SetOperator(query.MatchQueryOperatorAnd)
	return match
}

// materialize turns a hit into a result plus the collapse key it deduplicates
// under. Country is resolved from the border tree at query time, like the Rust
// server does, instead of being stored per document.
func (g *Geocoder) materialize(hit *search.DocumentMatch, includePolygon bool) (Result, string, bool) {
	fields := hit.Fields
	display := fieldString(fields, fieldDisplay)
	kind := fieldString(fields, fieldKind)
	lat := fieldFloat(fields, fieldLat)
	lon := fieldFloat(fields, fieldLon)

	item := Result{
		AddressString: display,
		Score:         hit.Score,
		Point:         [2]float64{lat, lon},
		GeoType:       kind,
	}

	switch kind {
	case KindZone:
		zoneIndex := int(fieldFloat(fields, fieldZoneIdx))
		if zoneIndex < 0 || zoneIndex >= len(g.zones) {
			return item, "", false
		}
		zone := g.zones[zoneIndex]
		item.AddressString = zone.Name.Value()
		if includePolygon && len(zone.Polygon) > 0 {
			item.Multipolygon = zone.Polygon
		}
		// Two same-named zones must stay distinct results.
		key := fmt.Sprintf("%s\x01%d", kind, zoneIndex)
		return item, key, true
	default:
		if country, ok := g.source.CountryAt(lat, lon); ok {
			item.Country = country
		}
		key := kind + "\x01" + strings.ToLower(item.Country) + "\x01" + strings.ToLower(item.AddressString)
		return item, key, true
	}
}

func fieldString(fields map[string]any, name string) string {
	if fields == nil {
		return ""
	}
	value, _ := fields[name].(string)
	return value
}

func fieldFloat(fields map[string]any, name string) float64 {
	if fields == nil {
		return 0
	}
	switch value := fields[name].(type) {
	case float64:
		return value
	case int64:
		return float64(value)
	default:
		return 0
	}
}
