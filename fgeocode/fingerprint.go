package fgeocode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const (
	fingerprintFileName = "rgeocache-fgeocode.json"
	// fingerprintFormat invalidates persisted indexes whose documents carry a
	// meaning that changed with the code (schema, field semantics).
	fingerprintFormat = 1
)

// fingerprint captures the cache identity an index was built from. Documents
// store zone indexes and coordinates that are only meaningful for one exact
// cache, so a persisted index is reused only when the fingerprint matches.
type fingerprint struct {
	Format      int    `json:"format"`
	DateCreated string `json:"date_created"`
	Locale      string `json:"locale"`
	NumPoints   int    `json:"num_points"`
	NumZones    int    `json:"num_zones"`
	CacheSize   int64  `json:"cache_size"`
}

func (g *Geocoder) fingerprint() fingerprint {
	meta := g.source.Metadata()
	fp := fingerprint{
		Format:    fingerprintFormat,
		Locale:    meta.Locale,
		NumPoints: g.source.NumPoints(),
		NumZones:  len(g.zones),
	}
	if !meta.DateCreated.IsZero() {
		fp.DateCreated = meta.DateCreated.UTC().Format(time.RFC3339)
	}
	if g.cfg.CacheFile != "" {
		if info, err := os.Stat(g.cfg.CacheFile); err == nil {
			fp.CacheSize = info.Size()
		}
	}
	return fp
}

func readFingerprint(dir string) (fingerprint, error) {
	var fp fingerprint
	data, err := os.ReadFile(filepath.Join(dir, fingerprintFileName))
	if err != nil {
		return fp, err
	}
	if err := json.Unmarshal(data, &fp); err != nil {
		return fp, err
	}
	return fp, nil
}

func writeFingerprint(dir string, fp fingerprint) error {
	data, err := json.MarshalIndent(fp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, fingerprintFileName), append(data, '\n'), 0o644)
}
