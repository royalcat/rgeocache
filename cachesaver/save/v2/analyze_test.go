package savev2

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"unique"

	"github.com/dustin/go-humanize"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
)

// analyzeSaved runs PrintCacheAnalysis over the body of a cache file (the bytes
// after the magic + compatibility level) and returns what it printed.
func analyzeSaved(t *testing.T, buf *bytes.Buffer) string {
	t.Helper()

	if buf.Len() < 8 {
		t.Fatalf("cache buffer too short: %d bytes", buf.Len())
	}
	body := buf.Bytes()[8:]

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	runErr := PrintCacheAnalysis(bytes.NewReader(body))

	_ = w.Close()
	os.Stdout = orig

	out, readErr := io.ReadAll(r)
	_ = r.Close()
	if readErr != nil {
		t.Fatalf("reading captured stdout: %v", readErr)
	}
	if runErr != nil {
		t.Fatalf("PrintCacheAnalysis: %v", runErr)
	}
	return string(out)
}

func graphTestPoints() []cachemodel.Point {
	roadInfo := cachemodel.Info{
		Name:        unique.Make(""),
		Street:      unique.Make("Road"),
		HouseNumber: unique.Make(""),
		City:        unique.Make(""),
		Region:      unique.Make(""),
		Weight:      5,
		Type:        cachemodel.GeoObjectRoad,
	}
	return []cachemodel.Point{
		{X: 0, Y: 0, Data: roadInfo},
		{X: 0, Y: 1, Data: roadInfo},
		{X: 1, Y: 0, Data: roadInfo},
		{X: 1, Y: 1, Data: roadInfo},
	}
}

func graphTestEdges() []cachemodel.GraphEdge {
	return []cachemodel.GraphEdge{
		{
			FromNode: 1, ToNode: 2,
			Street: unique.Make("First Street"), Name: unique.Make(""),
			Class: cachemodel.GraphClassPrimary, Oneway: cachemodel.GraphOnewayForward,
		},
		{
			FromNode: 2, ToNode: 3,
			Street: unique.Make("First Street"), Name: unique.Make(""),
			Class: cachemodel.GraphClassPrimary, Oneway: cachemodel.GraphOnewayBoth,
		},
		{
			FromNode: 3, ToNode: 1,
			Street: unique.Make("Second Street"), Name: unique.Make(""),
			Class: cachemodel.GraphClassSecondary, Oneway: cachemodel.GraphOnewayBackward,
		},
	}
}

func TestPrintCacheAnalysisGraph(t *testing.T) {
	var buf bytes.Buffer
	writeCacheFilePrefix(&buf)
	if err := Save(&buf, graphItems(graphTestPoints(), graphTestEdges()), sliceToSeq([]cachemodel.Zone{}), makeTestMetadata()); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	section, err := FindGraphSection(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("FindGraphSection failed: %v", err)
	}

	out := analyzeSaved(t, &buf)

	if want := "Road graph edges: 3"; !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
	for _, want := range []string{"Edge indices:", "Edge coords:", "Edge offsets:", "Edge records:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if want := "Road graph total: " + humanize.Bytes(section.TotalSize); !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}

	// The grand total must account for every byte of the cache body, graph included.
	if want := "Total uncompressed size: " + humanize.Bytes(uint64(buf.Len()-8)); !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}

func TestPrintCacheAnalysisNoGraph(t *testing.T) {
	var buf bytes.Buffer
	writeCacheFilePrefix(&buf)
	if err := Save(&buf, pointsToItems(nil), sliceToSeq([]cachemodel.Zone{}), makeTestMetadata()); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	out := analyzeSaved(t, &buf)

	if !strings.Contains(out, "Road graph: none") {
		t.Errorf("output missing %q:\n%s", "Road graph: none", out)
	}
	if want := "Total uncompressed size: " + humanize.Bytes(uint64(buf.Len()-8)); !strings.Contains(out, want) {
		t.Errorf("output missing %q:\n%s", want, out)
	}
}
