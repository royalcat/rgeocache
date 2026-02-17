package savev1

import (
	"bytes"
	"iter"
	"strconv"
	"testing"
	"time"
	"unique"

	"github.com/paulmach/orb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

func TestSaveLoad(t *testing.T) {
	originalPoints := []cachemodel.Point{
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
	// Create more points to test chunking
	for i := range 2000 {
		originalPoints = append(originalPoints, cachemodel.Point{
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
	originalZones := []cachemodel.Zone{}
	originalMeta := cachemodel.Metadata{
		Version:     123,
		DateCreated: time.Unix(1609459200, 0),
		Locale:      "en",
	}

	// Create a buffer to store the serialized data
	var buf bytes.Buffer

	// Save cache to buffer
	err := Save(&buf, slicesIter(originalPoints), slicesIter(originalZones), originalMeta)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load cache from buffer
	// TODO originalMetadata tests
	pointsIter, zonesIter, metadata, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Compare the original and loaded caches
	if originalMeta.Locale != metadata.Locale {
		t.Errorf("Locale don't match:\nOriginal: %v\nLoaded: %v", originalMeta.Locale, metadata.Locale)
	}

	if !originalMeta.DateCreated.Equal(metadata.DateCreated) {
		t.Errorf("DateCreated don't match:\nOriginal: %v\nLoaded: %v", originalMeta.DateCreated, metadata.DateCreated)
	}

	if originalMeta.Version != metadata.Version {
		t.Errorf("Version don't match:\nOriginal: %v\nLoaded: %v", originalMeta.Version, metadata.Version)
	}

	points := []cachemodel.Point{}
	for point, err := range pointsIter {
		if err != nil {
			t.Errorf("Error iterating points: %v", err)
			break
		}
		points = append(points, point)
	}
	if len(originalPoints) != len(points) {
		t.Errorf("Points count doesn't match: expected %d, got %d", len(originalPoints), len(points))
	} else {
		for i, originalPoint := range originalPoints {
			loadedPoint := points[i]
			if !pointsEqual(originalPoint, loadedPoint) {
				t.Errorf("Point %d doesn't match:\nOriginal: %+v\nLoaded: %+v", i, originalPoint, loadedPoint)
				break
			}
		}
	}

	zones := []cachemodel.Zone{}
	for zone, err := range zonesIter {
		if err != nil {
			t.Errorf("Error iterating points: %v", err)
			break
		}
		zones = append(zones, zone)
	}

	if len(originalZones) != len(zones) {
		t.Errorf("Zones count doesn't match: expected %d, got %d", len(originalZones), len(zones))
	} else {
		for i, originalZone := range originalZones {
			loadedZone := zones[i]
			if originalZone.Name == loadedZone.Name && originalZone.Bounds == loadedZone.Bounds && orb.Equal(originalZone.Polygon, loadedZone.Polygon) {
				t.Errorf("Zone %d doesn't match:\nOriginal: %+v\nLoaded: %+v", i, originalZone, loadedZone)
				break
			}
		}
	}
}

func pointsEqual(a, b cachemodel.Point) bool {
	return a == b
}

func slicesIter[T any](slice []T) iter.Seq[T] {
	return func(yield func(T) bool) {
		i := 0
		for i < len(slice) {
			if !yield(slice[i]) {
				return
			}
			i++
		}
	}
}
