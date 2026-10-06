package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unique"

	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/fgeocode"
	"github.com/valyala/fasthttp"
)

// testFGeoSource is an in-memory fgeocode.Source for handler tests.
type testFGeoSource struct {
	points []cachemodel.Point
	zones  []cachemodel.Zone
}

func (s *testFGeoSource) ForEachPoint(fn func(cachemodel.Point) bool) {
	for _, p := range s.points {
		if !fn(p) {
			return
		}
	}
}

func (s *testFGeoSource) Zones() []cachemodel.Zone { return s.zones }
func (s *testFGeoSource) NumPoints() int           { return len(s.points) }
func (s *testFGeoSource) Metadata() cachemodel.Metadata {
	return cachemodel.Metadata{Locale: "en", DateCreated: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}
func (s *testFGeoSource) CountryAt(lat, lon float64) (string, bool) { return "Testland", true }

func fgeoTestPoint(lat, lon float64, name, street, house, city, region string, weight uint8, objectType cachemodel.GeoObjectType) cachemodel.Point {
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

func newTestFGeo(t *testing.T) *fgeocode.Geocoder {
	t.Helper()
	src := &testFGeoSource{
		points: []cachemodel.Point{
			fgeoTestPoint(1, 1, "", "High Street", "12", "Springfield", "West", 10, cachemodel.GeoObjectBuilding),
		},
		zones: []cachemodel.Zone{
			{
				Type:    cachemodel.ZoneRegion,
				Name:    unique.Make("Test Region"),
				Bounds:  orb.Bound{Min: orb.Point{1, 1}, Max: orb.Point{2, 2}},
				Polygon: orb.MultiPolygon{orb.Polygon{orb.Ring{orb.Point{1, 1}, orb.Point{2, 1}, orb.Point{2, 2}, orb.Point{1, 1}}}},
			},
		},
	}
	g := fgeocode.New(fgeocode.Config{Source: src})
	g.Build(context.Background())
	if err := g.Ready(); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func newFGeoServer(t *testing.T, g *fgeocode.Geocoder) *server {
	t.Helper()
	return &server{
		fgeo:                            g,
		metricFGeoCallCount:             must(meter.Int64Counter("fgeocode_call_total")),
		metricFGeoAutocompleteCallCount: must(meter.Int64Counter("fgeocode_autocomplete_call_total")),
	}
}

func fgeoRequest(uri string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI(uri)
	return ctx
}

func TestFGeoCodeHandler(t *testing.T) {
	s := newFGeoServer(t, newTestFGeo(t))

	ctx := fgeoRequest("/fgeocode/search?q=High+Street")
	s.FGeoCodeHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var resp fgeocode.SearchResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Results))
	}
	if got := resp.Results[0].AddressString; got != "West, Springfield, High Street, 12" {
		t.Errorf("unexpected address_string: %q", got)
	}
	if resp.Results[0].Point != [2]float64{1, 1} {
		t.Errorf("unexpected point: %v", resp.Results[0].Point)
	}
}

func TestFGeoCodeHandlerZoneDefaultsToPolygon(t *testing.T) {
	s := newFGeoServer(t, newTestFGeo(t))

	ctx := fgeoRequest("/fgeocode/search?q=Test+Region&kind=zone")
	s.FGeoCodeHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var resp fgeocode.SearchResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Results) != 1 || len(resp.Results[0].Multipolygon) == 0 {
		t.Fatalf("expected one zone result with polygon, got %+v", resp.Results)
	}

	ctx = fgeoRequest("/fgeocode/search?q=Test+Region&kind=zone&include_polygon=false")
	s.FGeoCodeHandler(ctx)
	var respWithout fgeocode.SearchResponse
	if err := json.Unmarshal(ctx.Response.Body(), &respWithout); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(respWithout.Results) != 1 || len(respWithout.Results[0].Multipolygon) != 0 {
		t.Fatalf("expected zone without polygon, got %+v", respWithout.Results)
	}
}

func TestFGeoCodeHandlerValidation(t *testing.T) {
	s := newFGeoServer(t, newTestFGeo(t))

	ctx := fgeoRequest("/fgeocode/search")
	s.FGeoCodeHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Errorf("expected 400 without query, got %d", ctx.Response.StatusCode())
	}

	long := ""
	for len(long) <= fgeocode.MaxQueryLen {
		long += "a"
	}
	ctx = fgeoRequest("/fgeocode/search?q=" + long)
	s.FGeoCodeHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Errorf("expected 400 for long query, got %d", ctx.Response.StatusCode())
	}

	ctx = fgeoRequest("/fgeocode/search?q=Street&limit=nope")
	s.FGeoCodeHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Errorf("expected 400 for invalid limit, got %d", ctx.Response.StatusCode())
	}
}

func TestFGeoCodeHandlerBuilding(t *testing.T) {
	g := fgeocode.New(fgeocode.Config{Source: &testFGeoSource{}})
	t.Cleanup(func() { _ = g.Close() })

	s := newFGeoServer(t, g)
	ctx := fgeoRequest("/fgeocode/search?q=High")
	s.FGeoCodeHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Header.Peek("Retry-After")); got != "5" {
		t.Errorf("expected Retry-After 5, got %q", got)
	}
}

func TestFGeoCodeHandlerDisabled(t *testing.T) {
	s := newFGeoServer(t, nil)
	ctx := fgeoRequest("/fgeocode/search?q=High")
	s.FGeoCodeHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", ctx.Response.StatusCode())
	}
}

func TestFGeoAutocompleteHandler(t *testing.T) {
	s := newFGeoServer(t, newTestFGeo(t))

	ctx := fgeoRequest("/fgeocode/autocomplete?q=spr")
	s.FGeoAutocompleteHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var resp fgeocode.AutocompleteResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	found := false
	for _, suggestion := range resp.Suggestions {
		if suggestion.Text == "springfield" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'springfield' suggestion, got %+v", resp.Suggestions)
	}

	ctx = fgeoRequest("/fgeocode/autocomplete")
	s.FGeoAutocompleteHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Errorf("expected 400 without q, got %d", ctx.Response.StatusCode())
	}
}

func TestFGeoDemoHandler(t *testing.T) {
	ctx := fgeoRequest("/fgeocode/demo")
	FGeoDemoHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Header.ContentType()); got != "text/html; charset=utf-8" {
		t.Errorf("unexpected content type %q", got)
	}
	body := string(ctx.Response.Body())
	for _, want := range []string{`id="q"`, "/fgeocode/search"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q", want)
		}
	}
}
