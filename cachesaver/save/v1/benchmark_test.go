package savev1

import (
	"bytes"
	"io"
	"strconv"
	"testing"
	"time"
	"unique"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

func generateTestData(pointCount int) ([]cachemodel.Point, []cachemodel.Zone, cachemodel.Metadata) {
	points := []cachemodel.Point{
		{
			X: 40.7128,
			Y: -74.0060,
			Data: cachemodel.Info{
				Name:        unique.Make("Point 1"),
				Street:      unique.Make("Street 1"),
				HouseNumber: unique.Make("123"),
				City:        unique.Make("City 1"),
				Region:      unique.Make("Region 1"),
				Weight:      1,
			},
		},
		{
			X: 34.0522,
			Y: -118.2437,
			Data: cachemodel.Info{
				Name:        unique.Make("Point 2"),
				Street:      unique.Make("Street 2"),
				HouseNumber: unique.Make("456"),
				City:        unique.Make("City 2"),
				Region:      unique.Make("Region 2"),
				Weight:      2,
			},
		},
	}

	for i := range pointCount {
		points = append(points, cachemodel.Point{
			X: float64(i) / 1000,
			Y: float64(i) / -1000,
			Data: cachemodel.Info{
				Name:        unique.Make("Extra Point"),
				Street:      unique.Make(strconv.Itoa(i)),
				HouseNumber: unique.Make(strconv.Itoa(i)),
				City:        unique.Make(strconv.Itoa(i)),
				Region:      unique.Make(strconv.Itoa(i)),
				Weight:      uint8(i % 10),
			},
		})
	}

	zones := []cachemodel.Zone{}

	meta := cachemodel.Metadata{
		Version:     1,
		Locale:      "en",
		DateCreated: time.Unix(1609459200, 0),
	}

	return points, zones, meta
}

func BenchmarkSave(b *testing.B) {
	points, zones, meta := generateTestData(2000)

	// Pre-run once to verify correctness
	var buf bytes.Buffer
	if err := Save(&buf, slicesIter(points), slicesIter(zones), meta); err != nil {
		b.Fatalf("Save failed during setup: %v", err)
	}

	b.ResetTimer()

	for b.Loop() {
		err := Save(io.Discard, slicesIter(points), slicesIter(zones), meta)
		if err != nil {
			b.Fatalf("Save failed: %v", err)
		}
	}
}

func BenchmarkLoad(b *testing.B) {
	points, zones, meta := generateTestData(2000)

	var buf bytes.Buffer
	if err := Save(&buf, slicesIter(points), slicesIter(zones), meta); err != nil {
		b.Fatalf("Save failed during setup: %v", err)
	}
	serialized := buf.Bytes()

	b.ResetTimer()

	for b.Loop() {
		reader := bytes.NewReader(serialized)
		pointsIter, zonesIter, _, err := Load(reader)
		if err != nil {
			b.Fatalf("Load failed: %v", err)
		}

		for _, err := range pointsIter {
			if err != nil {
				b.Fatalf("Error iterating points: %v", err)
			}
		}
		for _, err := range zonesIter {
			if err != nil {
				b.Fatalf("Error iterating zones: %v", err)
			}
		}
	}
}
