package geoparser

import (
	"testing"
)

func TestInterpolateRoadSegment(t *testing.T) {
	tests := []struct {
		name                   string
		lon1, lat1, lon2, lat2 float64
		want                   int
	}{
		{"zero length segment", 0, 0, 0, 0, 0},
		{"short segment is not interpolated", 0, 0, 0, 0.001, 0}, // ~111 m
		{"segment just under the cap stays empty", 0, 0, 0, 0.0013, 0},
		{"long segment gets evenly spaced points", 0, 0, 0, 0.004, 2},   // ~445 m
		{"longitude is weighted by cos(latitude)", 0, 60, 0.003, 60, 1}, // ~167 m at 60°N
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			points := interpolateRoadSegment(tt.lon1, tt.lat1, tt.lon2, tt.lat2)
			if len(points) != tt.want {
				t.Fatalf("interpolateRoadSegment returned %d points, want %d", len(points), tt.want)
			}

			prevLon, prevLat := tt.lon1, tt.lat1
			for _, p := range points {
				if step := roadSegmentMeters(prevLon, prevLat, p.X(), p.Y()); step > roadInterpolationMeters {
					t.Errorf("step of %.1f m exceeds the %.0f m cap", step, roadInterpolationMeters)
				}
				if !between(tt.lon1, tt.lon2, p.X()) || !between(tt.lat1, tt.lat2, p.Y()) {
					t.Errorf("point %v leaves the segment", p)
				}
				prevLon, prevLat = p.X(), p.Y()
			}

			if step := roadSegmentMeters(prevLon, prevLat, tt.lon2, tt.lat2); step > roadInterpolationMeters {
				t.Errorf("last step of %.1f m exceeds the %.0f m cap", step, roadInterpolationMeters)
			}
		})
	}
}

// between reports whether v lies within [a, b] (or [b, a]).
func between(a, b, v float64) bool {
	lo, hi := a, b
	if lo > hi {
		lo, hi = hi, lo
	}
	const epsilon = 1e-12
	return v >= lo-epsilon && v <= hi+epsilon
}
