package server

import (
	"encoding/json"
	"strings"
	"testing"
	"unique"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/valyala/fasthttp"
)

func rgeoTestPoint(lon, lat float64, weight uint8, street, house, city string) cachemodel.Point {
	return cachemodel.Point{
		X: lon,
		Y: lat,
		Data: cachemodel.Info{
			Name:        unique.Make(""),
			Street:      unique.Make(street),
			HouseNumber: unique.Make(house),
			City:        unique.Make(city),
			Region:      unique.Make(""),
			Weight:      weight,
		},
	}
}

func newTestRGeoServer(t *testing.T) *server {
	t.Helper()
	return &server{
		rgeo: geocoder.NewGeoCoderFromPoints([]cachemodel.Point{
			rgeoTestPoint(0, 0, 10, "Alpha", "1", "Town"),
			rgeoTestPoint(0.002, 0, 5, "Beta", "", "Town"),
			rgeoTestPoint(0.5, 0, 10, "Far", "", ""),
		}),
		metricRGeoPointsCallCount: must(meter.Int64Counter("rgeocode_points_call_total")),
	}
}

func TestRGeoPointsHandler(t *testing.T) {
	s := newTestRGeoServer(t)

	ctx := fgeoRequest("/rgeocode/points?lat=0&lon=0&radius=0.01")
	s.RGeoPointsHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if got := string(ctx.Response.Header.ContentType()); got != "application/json" {
		t.Errorf("unexpected content type %q", got)
	}

	var resp pointsFeatureCollection
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Type != "FeatureCollection" {
		t.Errorf("type = %q, want FeatureCollection", resp.Type)
	}
	if resp.Total != 2 || resp.Truncated || len(resp.Features) != 2 {
		t.Fatalf("expected 2 of 2 features, got %d (total %d, truncated %v)",
			len(resp.Features), resp.Total, resp.Truncated)
	}

	first := resp.Features[0]
	if first.Geometry.Type != "Point" || first.Geometry.Coordinates != [2]float64{0, 0} {
		t.Errorf("unexpected geometry %+v", first.Geometry)
	}
	if first.Properties.Street != "Alpha" || first.Properties.HouseNumber != "1" ||
		first.Properties.City != "Town" || first.Properties.GeoType != "building" {
		t.Errorf("unexpected properties %+v", first.Properties)
	}
	if resp.Features[1].Properties.GeoType != "road" {
		t.Errorf("second feature geo_type = %q, want road", resp.Features[1].Properties.GeoType)
	}
}

func TestRGeoPointsHandlerLimitAndKind(t *testing.T) {
	s := newTestRGeoServer(t)

	ctx := fgeoRequest("/rgeocode/points?lat=0&lon=0&radius=0.01&limit=1")
	s.RGeoPointsHandler(ctx)
	var resp pointsFeatureCollection
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.Truncated || resp.Total != 2 || len(resp.Features) != 1 {
		t.Fatalf("expected 1 of 2 features (truncated), got %d (total %d, truncated %v)",
			len(resp.Features), resp.Total, resp.Truncated)
	}
	if resp.Features[0].Properties.Street != "Alpha" {
		t.Errorf("limit must keep the nearest feature, got %q", resp.Features[0].Properties.Street)
	}

	ctx = fgeoRequest("/rgeocode/points?lat=0&lon=0&radius=0.01&kind=road")
	s.RGeoPointsHandler(ctx)
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Total != 1 || len(resp.Features) != 1 || resp.Features[0].Properties.Street != "Beta" {
		t.Fatalf("kind=road returned %+v", resp.Features)
	}
}

func TestRGeoPointsHandlerValidation(t *testing.T) {
	s := newTestRGeoServer(t)

	for _, uri := range []string{
		"/rgeocode/points",
		"/rgeocode/points?lon=0",
		"/rgeocode/points?lat=0",
		"/rgeocode/points?lat=x&lon=0",
		"/rgeocode/points?lat=0&lon=y",
		"/rgeocode/points?lat=91&lon=0",
		"/rgeocode/points?lat=0&lon=181",
		"/rgeocode/points?lat=0&lon=0&radius=0",
		"/rgeocode/points?lat=0&lon=0&radius=-1",
		"/rgeocode/points?lat=0&lon=0&radius=nan",
		"/rgeocode/points?lat=0&lon=0&limit=abc",
	} {
		ctx := fgeoRequest(uri)
		s.RGeoPointsHandler(ctx)
		if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d: %s", uri, ctx.Response.StatusCode(), ctx.Response.Body())
		}
	}

	// A radius above the maximum is clamped, not rejected.
	ctx := fgeoRequest("/rgeocode/points?lat=0&lon=0&radius=5")
	s.RGeoPointsHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("oversized radius: expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

func TestRGeoDemoHandler(t *testing.T) {
	ctx := fgeoRequest("/rgeocode/demo")
	RGeoDemoHandler(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d", ctx.Response.StatusCode())
	}
	if got := string(ctx.Response.Header.ContentType()); got != "text/html; charset=utf-8" {
		t.Errorf("unexpected content type %q", got)
	}
	body := string(ctx.Response.Body())
	for _, want := range []string{`id="radius"`, "/rgeocode/points"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q", want)
		}
	}
}
