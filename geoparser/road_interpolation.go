package geoparser

import (
	"math"

	"github.com/paulmach/orb"
)

// roadInterpolationMeters caps the distance between consecutive road fallback
// points on a way. OSM ways may run a long distance between shape nodes; where
// a span is longer, intermediate points are interpolated so reverse geocoding
// keeps a road point no farther apart than the old 150 m resampling did. It
// mirrors graphMaxEdgeMeters in cachesaver/save/v2.
const roadInterpolationMeters = 150.0

// metersPerDegreeLat is the mean length of one degree of latitude, matching
// the graph simplifier's metric.
const metersPerDegreeLat = 111_320.0

// roadSegmentMeters returns the approximate length of a road segment in
// metres, weighing longitude by cos(latitude) so distances are isotropic.
func roadSegmentMeters(lon1, lat1, lon2, lat2 float64) float64 {
	scale := math.Cos((lat1 + lat2) * 0.5 * math.Pi / 180)
	dx := (lon2 - lon1) * metersPerDegreeLat * scale
	dy := (lat2 - lat1) * metersPerDegreeLat
	return math.Hypot(dx, dy)
}

// interpolateRoadSegment returns the interior points of the segment from
// (lon1, lat1) to (lon2, lat2), evenly spaced so every step stays within
// roadInterpolationMeters. The endpoints themselves are not included.
func interpolateRoadSegment(lon1, lat1, lon2, lat2 float64) []orb.Point {
	steps := int(math.Ceil(roadSegmentMeters(lon1, lat1, lon2, lat2) / roadInterpolationMeters))
	if steps <= 1 {
		return nil
	}

	points := make([]orb.Point, 0, steps-1)
	for i := 1; i < steps; i++ {
		t := float64(i) / float64(steps)
		points = append(points, orb.Point{lon1 + (lon2-lon1)*t, lat1 + (lat2-lat1)*t})
	}
	return points
}
