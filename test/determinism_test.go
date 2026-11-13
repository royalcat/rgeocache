package test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/geoparser"
	"golang.org/x/exp/mmap"
)

// TestGenerationIsDeterministic generates the same cache with different thread
// counts and checks that the files are byte-identical. Output must depend only
// on the input and the configuration, never on scheduling.
func TestGenerationIsDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip("generates the London cache several times")
	}
	if err := DownloadTestOSMFile(LondonFileURL, LondonFileName); err != nil {
		t.Fatal(err)
	}

	file, err := mmap.Open(LondonFileName)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	osmdb, err := osmpbfdb.OpenDB(file, osmpbfdb.Config{IndexDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer osmdb.Close()

	generate := func(threads int) string {
		t.Helper()
		out := filepath.Join(t.TempDir(), "cache.rgc")
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()

		config := geoparser.ConfigDefault()
		config.Threads = threads
		config.DateCreated = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		gg, err := geoparser.NewGeoGen(osmdb, config)
		if err != nil {
			t.Fatal(err)
		}
		if err := gg.ParseOSMData([]geoparser.ParseOutput{{Format: "v2", Writer: f}}); err != nil {
			t.Fatal(err)
		}
		return hashFile(t, out)
	}

	want := generate(1)
	for _, threads := range []int{2, 4, 8, 8} {
		if got := generate(threads); got != want {
			t.Fatalf("cache generated with %d threads differs from the 1-thread cache: %s != %s", threads, got, want)
		}
	}
}

func hashFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}
