package savev1

import (
	"bytes"
	"reflect"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	// Create test data
	originalCache := Cache{
		Streets: []string{"Main St", "Broadway", "Park Ave"},
		Cities:  []string{"New York", "Los Angeles", "Chicago"},
		Regions: []string{"NY", "CA", "IL"},
		Points: []Point{
			{
				Lat:         40.7128,
				Lon:         -74.0060,
				Name:        "Point 1",
				Street:      0,
				HouseNumber: "123",
				City:        0,
				Region:      0,
			},
			{
				Lat:         34.0522,
				Lon:         -118.2437,
				Name:        "Point 2",
				Street:      1,
				HouseNumber: "456",
				City:        1,
				Region:      1,
			},
			{
				Lat:         41.8781,
				Lon:         -87.6298,
				Name:        "Point 3",
				Street:      2,
				HouseNumber: "789",
				City:        2,
				Region:      2,
			},
		},
	}

	// Create more points to test chunking
	for i := 0; i < 2000; i++ {
		originalCache.Points = append(originalCache.Points, Point{
			Lat:         float64(i) / 1000,
			Lon:         float64(i) / -1000,
			Name:        "Extra Point",
			Street:      uint32(i % 3),
			HouseNumber: "100",
			City:        uint32(i % 3),
			Region:      uint32(i % 3),
		})
	}

	// Create a buffer to store the serialized data
	var buf bytes.Buffer

	// Save cache to buffer
	err := Save(&buf, originalCache)
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Load cache from buffer
	loadedCache, err := Load(&buf)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Compare the original and loaded caches
	if !reflect.DeepEqual(originalCache.Streets, loadedCache.Streets) {
		t.Errorf("Streets don't match:\nOriginal: %v\nLoaded: %v", originalCache.Streets, loadedCache.Streets)
	}

	if !reflect.DeepEqual(originalCache.Cities, loadedCache.Cities) {
		t.Errorf("Cities don't match:\nOriginal: %v\nLoaded: %v", originalCache.Cities, loadedCache.Cities)
	}

	if !reflect.DeepEqual(originalCache.Regions, loadedCache.Regions) {
		t.Errorf("Regions don't match:\nOriginal: %v\nLoaded: %v", originalCache.Regions, loadedCache.Regions)
	}

	if len(originalCache.Points) != len(loadedCache.Points) {
		t.Errorf("Points count doesn't match: expected %d, got %d", len(originalCache.Points), len(loadedCache.Points))
	} else {
		for i, originalPoint := range originalCache.Points {
			loadedPoint := loadedCache.Points[i]
			if !pointsEqual(originalPoint, loadedPoint) {
				t.Errorf("Point %d doesn't match:\nOriginal: %+v\nLoaded: %+v", i, originalPoint, loadedPoint)
				break
			}
		}
	}
}

func pointsEqual(a, b Point) bool {
	return a.Lat == b.Lat &&
		a.Lon == b.Lon &&
		a.Name == b.Name &&
		a.Street == b.Street &&
		a.HouseNumber == b.HouseNumber &&
		a.City == b.City &&
		a.Region == b.Region
}
