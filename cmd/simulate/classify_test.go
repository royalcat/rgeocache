package main

import (
	"testing"

	"github.com/royalcat/rgeocache/geomodel"
)

func TestKindFromWeight(t *testing.T) {
	tests := []struct {
		weight uint8
		want   string
	}{
		{0, "none"},
		{2, "area"},
		{3, "area"},
		{5, "road"},
		{10, "building"},
		{7, "w7"},
	}

	for _, tt := range tests {
		if got := kindFromWeight(tt.weight); got != tt.want {
			t.Errorf("kindFromWeight(%d) = %q, want %q", tt.weight, got, tt.want)
		}
	}
}

func TestClassifyPair(t *testing.T) {
	info := func(weight uint8) geomodel.Info { return geomodel.Info{Weight: weight} }

	tests := []struct {
		name  string
		a, b  geomodel.Info
		equal bool
		want  string
	}{
		{"equal point answers", info(10), info(10), true, "match(point)"},
		{"equal border answers", info(0), info(0), true, "match(border)"},
		{"lost road", info(5), info(0), false, "lost_point A:road -> B:none"},
		{"lost building", info(10), info(0), false, "lost_point A:building -> B:none"},
		{"gained area", info(0), info(2), false, "gained_point A:none -> B:area"},
		{"building to road", info(10), info(5), false, "point_vs_point building->road"},
		{"road to road", info(5), info(5), false, "point_vs_point road->road"},
		{"border diff", info(0), info(0), false, "border_diff"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPair(tt.a, tt.b, tt.equal); got != tt.want {
				t.Errorf("classifyPair(...) = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassificationCounter(t *testing.T) {
	c := newClassificationCounter()
	for i := range 10 {
		c.add("lost_point A:road -> B:none", "weight: 5 vs 0", float64(i), 0)
	}
	c.add("match(point)", "", 0, 0)

	if got := c.counts["lost_point A:road -> B:none"]; got != 10 {
		t.Errorf("count = %d, want 10", got)
	}
	if got := len(c.examples["lost_point A:road -> B:none"]); got != maxCategoryExamples {
		t.Errorf("examples = %d, want %d", got, maxCategoryExamples)
	}
	if got := len(c.examples["match(point)"]); got != 0 {
		t.Errorf("match examples = %d, want 0", got)
	}
}
