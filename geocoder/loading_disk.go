package geocoder

import (
	"encoding/binary"
	"fmt"
	"unique"

	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	savev2 "github.com/royalcat/rgeocache/cachesaver/save/v2"
	"github.com/royalcat/rgeocache/internal/bordertree"
	"golang.org/x/exp/mmap"
)

// LoadGeoCoderFromFileDisk loads a v2 cache file using mmap for the spatial index.
// This is the default loading path for v2 caches — only string tables and zone
// polygons are loaded into memory; the KD-tree index is accessed via mmap.
//
// The returned RGeoCoderDisk must be closed after use to release the mmap mapping.
func LoadGeoCoderFromFileDisk(file string, opts ...Option) (*RGeoCoderDisk, error) {
	options := loadOptions(opts...)
	log := options.logger

	log.Info("Loading v2 geocoder from file via mmap", "file", file)

	reader, err := mmap.Open(file)
	if err != nil {
		return nil, fmt.Errorf("error mmapping cache file: %w", err)
	}

	// Verify magic bytes and compat level
	var magic [4]byte
	if _, err := reader.ReadAt(magic[:], 0); err != nil {
		reader.Close()
		return nil, fmt.Errorf("error reading magic bytes: %w", err)
	}
	if string(magic[:]) != "RGEO" {
		reader.Close()
		return nil, fmt.Errorf("invalid magic bytes: %q", string(magic[:]))
	}

	var compatBuf [4]byte
	if _, err := reader.ReadAt(compatBuf[:], 4); err != nil {
		reader.Close()
		return nil, fmt.Errorf("error reading compatibility level: %w", err)
	}
	compatLevel := binary.LittleEndian.Uint32(compatBuf[:])
	if compatLevel != savev2.COMPATIBILITY_LEVEL {
		reader.Close()
		return nil, fmt.Errorf("expected v2 cache (compat level %d), got %d", savev2.COMPATIBILITY_LEVEL, compatLevel)
	}

	// Load via mmap
	result, err := savev2.LoadMmap(reader)
	if err != nil {
		reader.Close()
		return nil, fmt.Errorf("error loading v2 cache via mmap: %w", err)
	}

	// Build border trees for region/country lookups
	regions := bordertree.NewBorderTree[unique.Handle[string]]()
	countries := bordertree.NewBorderTree[unique.Handle[string]]()
	for _, zone := range result.Zones {
		switch zone.Type {
		case cachemodel.ZoneRegion:
			regions.InsertBorder(zone.Name, zone.Polygon)
		case cachemodel.ZoneCountry:
			countries.InsertBorder(zone.Name, zone.Polygon)
		}
	}

	log.Info("v2 geocoder loaded via mmap",
		"num_points", result.DiskBush.NumPoints(),
		"num_zones", len(result.Zones),
		"node_size", result.DiskBush.NodeSize(),
	)

	return &RGeoCoderDisk{
		diskTree:          result.DiskBush,
		mmapReader:        reader,
		stringsIndex:      result.StringsIndex,
		stringsDataOffset: result.StringsDataOffset,
		regions:           regions,
		countries:         countries,
		searchRadius:      options.searchRadius,
		logger:            log,
	}, nil
}
