package main

import (
	"cmp"
	"fmt"
	"slices"
	"sync"

	"github.com/royalcat/rgeocache/geomodel"
)

// maxCategoryExamples caps how many example coordinates are kept per
// classification category.
const maxCategoryExamples = 3

// kindFromWeight names an answer's object kind. Weight 0 means no cached point
// was found and the server answered from its border trees alone; the other
// weights mirror the generator classes (10 buildings, 5 roads, 2/3 areas).
func kindFromWeight(weight uint8) string {
	switch weight {
	case 0:
		return "none"
	case 10:
		return "building"
	case 5:
		return "road"
	case 2, 3:
		return "area"
	default:
		return fmt.Sprintf("w%d", weight)
	}
}

// classifyPair labels one compared answer pair. equal reports whether every
// compared field matched; otherwise the label names which kinds the two
// servers disagreed on.
func classifyPair(a, b geomodel.Info, equal bool) string {
	if equal {
		if a.Weight > 0 {
			return "match(point)"
		}
		return "match(border)"
	}

	switch {
	case a.Weight > 0 && b.Weight > 0:
		return "point_vs_point " + kindFromWeight(a.Weight) + "->" + kindFromWeight(b.Weight)
	case a.Weight > 0:
		return "lost_point A:" + kindFromWeight(a.Weight) + " -> B:none"
	case b.Weight > 0:
		return "gained_point A:none -> B:" + kindFromWeight(b.Weight)
	default:
		return "border_diff"
	}
}

// classificationCounter accumulates one category per compared point pair and
// keeps a few example coordinates per category.
type classificationCounter struct {
	mu       sync.Mutex
	counts   map[string]int
	examples map[string][]mismatchEntry
}

func newClassificationCounter() *classificationCounter {
	return &classificationCounter{
		counts:   make(map[string]int),
		examples: make(map[string][]mismatchEntry),
	}
}

// add records one comparison result. detail is the rendered field diff (empty
// for matches); lat/lon identify the query that produced it.
func (c *classificationCounter) add(category, detail string, lat, lon float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.counts[category]++
	if detail == "" {
		return
	}
	if list := c.examples[category]; len(list) < maxCategoryExamples {
		c.examples[category] = append(list, mismatchEntry{Lat: lat, Lon: lon, Detail: detail})
	}
}

// report prints the category table, largest first, followed by example
// coordinates for the mismatching categories.
func (c *classificationCounter) report(total int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	type row struct {
		category string
		count    int
	}
	rows := make([]row, 0, len(c.counts))
	for category, count := range c.counts {
		rows = append(rows, row{category: category, count: count})
	}
	slices.SortFunc(rows, func(a, b row) int {
		if a.count != b.count {
			return cmp.Compare(b.count, a.count)
		}
		return cmp.Compare(a.category, b.category)
	})

	fmt.Println()
	fmt.Println("=== Mismatch classification ===")
	for _, r := range rows {
		share := 0.0
		if total > 0 {
			share = float64(r.count) / float64(total) * 100
		}
		fmt.Printf("  %-40s %8d  %6.3f%%\n", r.category, r.count, share)
	}

	printed := false
	for _, r := range rows {
		examples := c.examples[r.category]
		if len(examples) == 0 {
			continue
		}
		if !printed {
			fmt.Println("\nClassification examples:")
			printed = true
		}
		fmt.Printf("  %s:\n", r.category)
		for _, e := range examples {
			fmt.Printf("    (%.6f, %.6f): %s\n", e.Lat, e.Lon, e.Detail)
		}
	}
}
