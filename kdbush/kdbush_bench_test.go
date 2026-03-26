package kdbush

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func generatePoints(n int, bound float64) []Point[int] {
	rng := rand.New(rand.NewSource(42))
	pts := make([]Point[int], n)
	for i := range pts {
		pts[i] = Point[int]{
			X:    rng.Float64() * bound,
			Y:    rng.Float64() * bound,
			Data: i,
		}
	}
	return pts
}

var nodeSizes = []int{8, 16, 64, 128, 512}

const (
	numPoints = 100_000_000
	bound     = 1000.0
)

var queryPcts = []float64{0.01, 0.10, 0.50}

func Benchmark(b *testing.B) {
	pts := generatePoints(numPoints, bound)

	for _, nodeSize := range nodeSizes {
		bush := NewBush(pts, nodeSize)

		for _, queryPct := range queryPcts {
			half := bound * math.Sqrt(queryPct) / 2
			cx, cy := bound/2, bound/2
			minX, minY := cx-half, cy-half
			maxX, maxY := cx+half, cy+half

			name := fmt.Sprintf("Range/node%d/%.0fpct", nodeSize, queryPct*100)
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					bush.Range(minX, minY, maxX, maxY)
				}
			})
		}

		for _, radiusFrac := range queryPcts {
			radius := bound * radiusFrac
			cx, cy := bound/2, bound/2

			name := fmt.Sprintf("Within/node%d/%.0fpct", nodeSize, radiusFrac*100)
			b.Run(name, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					bush.Within(cx, cy, radius, func(p Point[int]) bool {
						return true
					})
				}
			})
		}
	}
}
