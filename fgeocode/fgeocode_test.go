package fgeocode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unique"

	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

// fakeSource is an in-memory Source for tests; it needs no cache file.
type fakeSource struct {
	points  []cachemodel.Point
	zones   []cachemodel.Zone
	locale  string
	created time.Time
	country string
}

func (f *fakeSource) ForEachPoint(fn func(cachemodel.Point) bool) {
	for _, p := range f.points {
		if !fn(p) {
			return
		}
	}
}

func (f *fakeSource) Zones() []cachemodel.Zone { return f.zones }
func (f *fakeSource) NumPoints() int           { return len(f.points) }
func (f *fakeSource) Metadata() cachemodel.Metadata {
	return cachemodel.Metadata{Locale: f.locale, DateCreated: f.created}
}

func (f *fakeSource) CountryAt(lat, lon float64) (string, bool) {
	if f.country == "" {
		return "", false
	}
	return f.country, true
}

func testPoint(lat, lon float64, name, street, house, city, region string, weight uint8, objectType cachemodel.GeoObjectType) cachemodel.Point {
	return cachemodel.Point{
		X: lon,
		Y: lat,
		Data: cachemodel.Info{
			Name:        unique.Make(name),
			Street:      unique.Make(street),
			HouseNumber: unique.Make(house),
			City:        unique.Make(city),
			Region:      unique.Make(region),
			Weight:      weight,
			Type:        objectType,
		},
	}
}

func testZone(name string, zoneType cachemodel.ZoneType) cachemodel.Zone {
	return cachemodel.Zone{
		Type:   zoneType,
		Name:   unique.Make(name),
		Bounds: orb.Bound{Min: orb.Point{1, 1}, Max: orb.Point{2, 2}},
		Polygon: orb.MultiPolygon{
			orb.Polygon{orb.Ring{
				orb.Point{1, 1}, orb.Point{2, 1}, orb.Point{2, 2}, orb.Point{1, 2}, orb.Point{1, 1},
			}},
		},
	}
}

func defaultSource() *fakeSource {
	return &fakeSource{
		locale:  "en",
		created: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		country: "Testland",
		points: []cachemodel.Point{
			testPoint(1.0, 1.0, "", "High Street", "12", "Springfield", "West", 10, cachemodel.GeoObjectBuilding),
			testPoint(2.0, 2.0, "Main Road", "", "", "Springfield", "West", 5, cachemodel.GeoObjectRoad),
			testPoint(3.0, 3.0, "Industrial Park", "", "", "Springfield", "West", 3, cachemodel.GeoObjectArea),
		},
		zones: []cachemodel.Zone{testZone("Test Region", cachemodel.ZoneRegion)},
	}
}

func buildTestGeocoder(t *testing.T, src Source, cfg Config) *Geocoder {
	t.Helper()
	cfg.Source = src
	g := New(cfg)
	g.Build(context.Background())
	if err := g.Ready(); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func TestSearchFreeText(t *testing.T) {
	g := buildTestGeocoder(t, defaultSource(), Config{})

	results, err := g.Search(SearchRequest{Query: "High Street", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
	first := results[0]
	if first.AddressString != "West, Springfield, High Street, 12" {
		t.Errorf("unexpected address_string: %q", first.AddressString)
	}
	if first.GeoType != KindBuilding {
		t.Errorf("expected building, got %q", first.GeoType)
	}
	if first.Country != "Testland" {
		t.Errorf("expected country Testland, got %q", first.Country)
	}
	if first.Point != [2]float64{1.0, 1.0} {
		t.Errorf("expected point [1 1], got %v", first.Point)
	}
}

func TestSearchStructured(t *testing.T) {
	g := buildTestGeocoder(t, defaultSource(), Config{})

	results, err := g.Search(SearchRequest{City: "Springfield", Street: "High", House: "12", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].AddressString != "West, Springfield, High Street, 12" {
		t.Errorf("unexpected address_string: %q", results[0].AddressString)
	}
}

func TestSearchKindFilter(t *testing.T) {
	g := buildTestGeocoder(t, defaultSource(), Config{})

	results, err := g.Search(SearchRequest{Query: "Springfield", Kinds: ParseKindFilter("road"), Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].GeoType != KindRoad {
		t.Errorf("expected road, got %q", results[0].GeoType)
	}

	aliasResults, err := g.Search(SearchRequest{Query: "Springfield", Kinds: ParseKindFilter("a"), Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(aliasResults) != 1 || aliasResults[0].GeoType != KindArea {
		t.Errorf("expected single area result, got %+v", aliasResults)
	}
}

func TestSearchOffsetAndLimit(t *testing.T) {
	src := defaultSource()
	for i, street := range []string{"Alpha Street", "Beta Street", "Gamma Street", "Delta Street"} {
		src.points = append(src.points, testPoint(10+float64(i), 10+float64(i), "", street, "1", "Townsville", "West", 10, cachemodel.GeoObjectBuilding))
	}
	g := buildTestGeocoder(t, src, Config{})

	results, err := g.Search(SearchRequest{Query: "Street", Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	offsetResults, err := g.Search(SearchRequest{Query: "Street", Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(offsetResults) != 2 {
		t.Fatalf("expected 2 offset results, got %d", len(offsetResults))
	}
	if offsetResults[0].AddressString == results[0].AddressString {
		t.Errorf("offset did not skip the first result: %q", offsetResults[0].AddressString)
	}
	seen := map[string]bool{}
	for _, r := range offsetResults {
		if seen[r.AddressString] {
			t.Errorf("duplicate result %q", r.AddressString)
		}
		seen[r.AddressString] = true
	}
}

func TestSearchZonePolygon(t *testing.T) {
	g := buildTestGeocoder(t, defaultSource(), Config{})

	results, err := g.Search(SearchRequest{Query: "Test Region", Kinds: ParseKindFilter("zone"), IncludePolygon: true, Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 zone result, got %d", len(results))
	}
	zone := results[0]
	if zone.GeoType != KindZone {
		t.Errorf("expected zone, got %q", zone.GeoType)
	}
	if zone.AddressString != "Test Region" {
		t.Errorf("unexpected zone address: %q", zone.AddressString)
	}
	if len(zone.Multipolygon) == 0 {
		t.Error("expected multipolygon by default")
	}
	if zone.Point != [2]float64{1.5, 1.5} {
		t.Errorf("expected center [1.5 1.5], got %v", zone.Point)
	}

	withoutPolygon, err := g.Search(SearchRequest{Query: "Test Region", Kinds: ParseKindFilter("zone"), IncludePolygon: false, Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(withoutPolygon) != 1 || len(withoutPolygon[0].Multipolygon) != 0 {
		t.Errorf("expected zone without polygon, got %+v", withoutPolygon)
	}
}

func TestSuggest(t *testing.T) {
	g := buildTestGeocoder(t, defaultSource(), Config{})

	suggestions, err := g.Suggest("spr", 10)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	found := false
	for _, s := range suggestions {
		if s.Text == "springfield" {
			found = true
			if s.DocFreq != 3 {
				t.Errorf("expected doc_freq 3, got %d", s.DocFreq)
			}
		}
	}
	if !found {
		t.Errorf("expected 'springfield' suggestion, got %+v", suggestions)
	}

	limited, err := g.Suggest("", 10)
	if err != nil {
		t.Fatalf("Suggest: %v", err)
	}
	if len(limited) != 0 {
		t.Errorf("expected no suggestions for empty prefix, got %+v", limited)
	}
}

func TestSearchNotReady(t *testing.T) {
	g := New(Config{Source: defaultSource()})
	t.Cleanup(func() { _ = g.Close() })

	if _, err := g.Search(SearchRequest{Query: "x"}); !errors.Is(err, ErrBuilding) {
		t.Errorf("expected ErrBuilding, got %v", err)
	}
	if _, err := g.Suggest("x", 10); !errors.Is(err, ErrBuilding) {
		t.Errorf("expected ErrBuilding, got %v", err)
	}
}

func TestIndexDirReuse(t *testing.T) {
	src := defaultSource()
	dir := t.TempDir()

	first := buildTestGeocoder(t, src, Config{IndexDir: dir})
	if first.Reused() {
		t.Error("first build should not be reused")
	}
	_ = first.Close()

	second := buildTestGeocoder(t, src, Config{IndexDir: dir})
	if !second.Reused() {
		t.Error("second build should reuse the index")
	}
	_ = second.Close()

	changed := defaultSource()
	changed.created = changed.created.Add(time.Hour)
	third := buildTestGeocoder(t, changed, Config{IndexDir: dir})
	if third.Reused() {
		t.Error("changed cache should force a rebuild")
	}
	_ = third.Close()
}

func TestIndexDirNonEmptyWithoutIndex(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "junk.txt"), []byte("junk"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	g := New(Config{Source: defaultSource(), IndexDir: dir})
	t.Cleanup(func() { _ = g.Close() })
	g.Build(context.Background())

	err := g.Ready()
	if err == nil {
		t.Fatal("expected build error for non-empty dir")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLocaleAnalyzer(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"official": "",
		"ru":       "ru",
		"ru-RU":    "ru",
		"en_US":    "en",
		"de":       "de",
		"uk":       "",
		"kk":       "",
	}
	for locale, want := range cases {
		if got := localeAnalyzer(locale); got != want {
			t.Errorf("localeAnalyzer(%q) = %q, want %q", locale, got, want)
		}
	}
}

func TestCanonicalHouse(t *testing.T) {
	cases := map[string]string{
		"":              "",
		"12":            "12",
		"12 корпус 1":   "12к1",
		"12 к 1":        "12к1",
		"12к1":          "12к1",
		"12-1":          "12-1",
		"12 строение 3": "12с3",
		"12 литера А":   "12ла",
	}
	for raw, want := range cases {
		if got := canonicalHouse(raw); got != want {
			t.Errorf("canonicalHouse(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestRussianStemming checks that a cache with a Russian locale selects the
// Bleve ru analyzer, so an inflected query still finds the street.
func TestRussianStemming(t *testing.T) {
	src := defaultSource()
	src.locale = "ru"
	src.points = []cachemodel.Point{
		testPoint(1, 1, "", "Тверская", "12", "Москва", "", 10, cachemodel.GeoObjectBuilding),
	}
	g := buildTestGeocoder(t, src, Config{})

	results, err := g.Search(SearchRequest{Query: "Тверской", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected a stemmed match, got %d results", len(results))
	}
}
