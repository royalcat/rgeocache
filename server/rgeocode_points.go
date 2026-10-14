package server

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/royalcat/rgeocache/fgeocode"
	"github.com/royalcat/rgeocache/geocoder"
	"github.com/valyala/fasthttp"
)

// errInvalidFloat is returned by queryFloat for malformed or non-finite input.
var errInvalidFloat = errors.New("invalid float value")

// Limits of GET /rgeocode/points. The default radius matches the default
// --search-radius, so a demo query shows the same neighbourhood a reverse
// lookup would consider.
const (
	pointsDefaultRadius = 0.01
	pointsMaxRadius     = 1.0
	pointsDefaultLimit  = 10_000
	pointsMaxLimit      = 50_000
)

// RGeoPointsHandler implements GET /rgeocode/points: every cached point within
// radius degrees of (lat, lon), as a GeoJSON FeatureCollection ordered nearest
// first, with the total number of matches and a truncation flag.
func (s *server) RGeoPointsHandler(ctx *fasthttp.RequestCtx) {
	s.metricRGeoPointsCallCount.Add(ctx, 1)

	args := ctx.QueryArgs()

	lat, ok, err := queryFloat(args, "lat")
	if err != nil || !ok {
		pointsBadRequest(ctx, "invalid latitude")
		return
	}
	if lat < -90 || lat > 90 {
		pointsBadRequest(ctx, "latitude out of range")
		return
	}

	lon, ok, err := queryFloat(args, "lon")
	if err != nil || !ok {
		pointsBadRequest(ctx, "invalid longitude")
		return
	}
	if lon < -180 || lon > 180 {
		pointsBadRequest(ctx, "longitude out of range")
		return
	}

	radius := pointsDefaultRadius
	if value, ok, err := queryFloat(args, "radius"); err != nil {
		pointsBadRequest(ctx, "invalid radius")
		return
	} else if ok {
		if value <= 0 {
			pointsBadRequest(ctx, "radius must be positive")
			return
		}
		radius = min(value, pointsMaxRadius)
	}

	limit, err := queryInt(args, "limit", pointsDefaultLimit)
	if err != nil {
		pointsBadRequest(ctx, "invalid limit")
		return
	}
	limit = min(max(limit, 1), pointsMaxLimit)

	filter := fgeocode.ParseKindFilter(string(args.Peek("kind")))
	var kinds geocoder.PointKind
	if filter.Building {
		kinds |= geocoder.PointKindBuilding
	}
	if filter.Road {
		kinds |= geocoder.PointKindRoad
	}
	if filter.Area {
		kinds |= geocoder.PointKindArea
	}

	points, total := s.rgeo.PointsInRadius(lat, lon, radius, limit, kinds)

	body, err := json.Marshal(newPointsFeatureCollection(points, total))
	if err != nil {
		ctx.Response.SetStatusCode(http.StatusInternalServerError)
		return
	}

	ctx.Response.Header.SetContentType("application/json")
	ctx.Response.SetStatusCode(http.StatusOK)
	ctx.Response.SetBody(body)
}

func pointsBadRequest(ctx *fasthttp.RequestCtx, message string) {
	ctx.Response.SetStatusCode(http.StatusBadRequest)
	ctx.Response.SetBodyString(message)
}

// queryFloat reads an optional float query parameter. ok is false when the
// parameter is absent.
func queryFloat(args *fasthttp.Args, key string) (value float64, ok bool, err error) {
	if !args.Has(key) {
		return 0, false, nil
	}
	value, err = strconv.ParseFloat(string(args.Peek(key)), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, true, errInvalidFloat
	}
	return value, true, nil
}

// GeoJSON response of GET /rgeocode/points. truncated and total are foreign
// members of the FeatureCollection, like /roadgraph/box's truncated.
type pointsFeatureCollection struct {
	Type      string          `json:"type"`
	Features  []pointsFeature `json:"features"`
	Truncated bool            `json:"truncated"`
	Total     int             `json:"total"`
}

type pointsFeature struct {
	Type       string              `json:"type"`
	Geometry   pointsPointGeometry `json:"geometry"`
	Properties pointsProperties    `json:"properties"`
}

type pointsPointGeometry struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"`
}

type pointsProperties struct {
	Name        string `json:"name"`
	Street      string `json:"street"`
	HouseNumber string `json:"house_number"`
	City        string `json:"city"`
	Region      string `json:"region"`
	Country     string `json:"country"`
	Weight      uint8  `json:"weight"`
	GeoType     string `json:"geo_type"`
}

func newPointsFeatureCollection(points []geocoder.PointInRadius, total int) pointsFeatureCollection {
	features := make([]pointsFeature, 0, len(points))
	for _, point := range points {
		features = append(features, pointsFeature{
			Type: "Feature",
			Geometry: pointsPointGeometry{
				Type:        "Point",
				Coordinates: [2]float64{point.X, point.Y},
			},
			Properties: pointsProperties{
				Name:        point.Info.Name,
				Street:      point.Info.Street,
				HouseNumber: point.Info.HouseNumber,
				City:        point.Info.City,
				Region:      point.Info.Region,
				Country:     point.Info.Country,
				Weight:      point.Info.Weight,
				GeoType:     point.Kind.String(),
			},
		})
	}
	return pointsFeatureCollection{
		Type:      "FeatureCollection",
		Features:  features,
		Truncated: total > len(points),
		Total:     total,
	}
}
