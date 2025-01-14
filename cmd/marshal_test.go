package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/royalcat/rgeocache/geomodel"
)

func TestGeoModelFastMarshal(t *testing.T) {
	buf := new(bytes.Buffer)
	model := geomodel.Info{
		Region:      "England",
		City:        "London",
		Street:      "Cannon Row",
		HouseNumber: "1",
	}
	writeGeoInfoFast(buf, model)

	expected, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(buf.Bytes(), expected) {
		t.Fatalf("expected %s; got %s", expected, buf.Bytes())
	}

	buf.Reset()
}

func TestGeoModelListFastMarshal(t *testing.T) {
	buf := new(bytes.Buffer)
	models := []geomodel.Info{
		{
			Region:      "England",
			City:        "London",
			Street:      "Cannon Row",
			HouseNumber: "1",
		},
		{
			Region:      "England",
			City:        "London",
			Street:      "Cannon Row",
			HouseNumber: "2",
		},
	}
	writeGeoInfoListFast(buf, models)

	expected, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(buf.Bytes(), expected) {
		t.Fatalf("expected %s; got %s", expected, buf.Bytes())
	}

	buf.Reset()
}

func BenchmarkGeoModelMarshal(b *testing.B) {
	models := []geomodel.Info{
		{
			Region:      "England",
			City:        "London",
			Street:      "Cannon Row",
			HouseNumber: "1",
		},
		{
			Region:      "England",
			City:        "London",
			Street:      "Cannon Row",
			HouseNumber: "2",
		},
	}

	b.Run("fast", func(b *testing.B) {
		buf := new(bytes.Buffer)
		for i := 0; i < b.N; i++ {
			writeGeoInfoListFast(buf, models)
			buf.Reset()
		}
	})

	b.Run("json", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := json.Marshal(models)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
