package geoparser

import (
	"io"
	"log/slog"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
	"unique"

	"github.com/puzpuzpuz/xsync/v3"
	"github.com/royalcat/osmpbfdb"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/internal/bordertree"
	"github.com/royalcat/rgeocache/internal/idset"
	"github.com/royalcat/rgeocache/internal/spool"
	"github.com/royalcat/rgeocache/internal/strtab"
	"golang.org/x/sync/errgroup"
)

type GeoGen struct {
	osmdb  osmpbfdb.OsmDB
	config Config

	placeIndex  *bordertree.BorderTree[string]
	regionIndex *bordertree.BorderTree[string]

	localizationCache *xsync.MapOf[string, string]

	parsedWays           *idset.Set
	parsedWaysDupes      atomic.Uint64
	parsedRelations      *idset.Set
	parsedRelationsDupes atomic.Uint64
	seenNodes            atomic.Int64
	seenWays             atomic.Int64
	seenRelations        atomic.Int64

	// synthKeySeq assigns unique node keys to interpolated road points. The
	// high bit is set so they can never collide with a real OSM node id.
	synthKeySeq atomic.Uint64

	parsedItems chan parseItem

	regionsMu sync.Mutex
	regions   []geomodel.Zone

	countriesMu sync.Mutex
	countries   []geomodel.Zone

	log *slog.Logger
}

func NewGeoGen(db osmpbfdb.OsmDB, config Config) (*GeoGen, error) {
	return &GeoGen{
		osmdb:  db,
		config: config,

		placeIndex:        bordertree.NewBorderTreeWithSelection[string](bordertree.Largest),
		regionIndex:       bordertree.NewBorderTreeWithSelection[string](bordertree.Smallest),
		localizationCache: xsync.NewMapOf[string, string](),

		parsedWays:      idset.New(),
		parsedRelations: idset.New(),

		regions:   []geomodel.Zone{},
		countries: []geomodel.Zone{},

		log: slog.Default(),
	}, nil
}

func (f *GeoGen) ResetCache() error {
	f.placeIndex = bordertree.NewBorderTreeWithSelection[string](bordertree.Largest)
	f.regionIndex = bordertree.NewBorderTreeWithSelection[string](bordertree.Smallest)
	f.localizationCache = xsync.NewMapOf[string, string]()
	runtime.GC()

	return nil
}

// syntheticNodeKey returns a node key that is unique across the whole run.
func (f *GeoGen) syntheticNodeKey() uint64 {
	return 1<<63 | f.synthKeySeq.Add(1)
}

type ParseOutput struct {
	Format string
	Writer io.Writer
}

// ParseOSMData parses the OSM database into an on-disk spool and then writes
// every requested output from it. Only the v2 format is supported here; v1
// caches are produced by cachesaver.ConvertV2ToV1 from a finished v2 file.
//
// The spool directory defaults to a temporary directory that is removed when
// ParseOSMData returns. When config.SpoolDir is set it is used instead and is
// removed on success (kept on failure for inspection).
func (f *GeoGen) ParseOSMData(outputs []ParseOutput) error {
	for _, output := range outputs {
		if output.Format != "v2" {
			return &UnsupportedFormatError{Format: output.Format}
		}
	}

	spoolDir := f.config.SpoolDir
	temporary := false
	if spoolDir == "" {
		dir, err := os.MkdirTemp("", "rgeocache-spool-*")
		if err != nil {
			return err
		}
		spoolDir = dir
		temporary = true
	}
	success := false
	defer func() {
		if success || temporary {
			_ = os.RemoveAll(spoolDir)
		}
	}()

	if err := os.MkdirAll(spoolDir, 0o755); err != nil {
		return err
	}

	sp, err := spool.Create(spoolDir)
	if err != nil {
		return err
	}
	table := strtab.New()

	f.parsedItems = make(chan parseItem, 10)

	var wg errgroup.Group
	wg.Go(func() error {
		return f.collectWorker(sp, table)
	})
	wg.Go(func() error {
		defer close(f.parsedItems)
		if err := f.fillRelCache(); err != nil {
			return err
		}
		return f.parseDatabase()
	})
	if err := wg.Wait(); err != nil {
		_ = sp.Close()
		return err
	}
	if err := sp.Close(); err != nil {
		return err
	}

	// The border trees and duplicate sets are parse-only and large. Release
	// them before the writer allocates its index arrays.
	f.placeIndex = nil
	f.regionIndex = nil
	f.localizationCache = nil
	f.parsedWays = nil
	f.parsedRelations = nil
	runtime.GC()

	spoolReader, err := spool.Open(spoolDir)
	if err != nil {
		return err
	}

	meta := cachemodel.Metadata{
		Version:     f.config.Version,
		Locale:      f.config.PreferredLocalization,
		DateCreated: time.Now().UTC(),
	}
	zones := f.buildZones()

	for _, output := range outputs {
		// The writer keeps several whole-cache index arrays live at once. A
		// lower GC target halves that headroom; the phases are I/O bound, so
		// the extra collections cost little.
		previous := debug.SetGCPercent(50)
		err := writeV2Output(spoolReader, table, zones, meta, output.Writer, spoolDir)
		debug.SetGCPercent(previous)
		if err != nil {
			return err
		}
	}

	success = true
	return nil
}

// UnsupportedFormatError is returned by ParseOSMData for formats it cannot
// stream from the spool.
type UnsupportedFormatError struct {
	Format string
}

func (e *UnsupportedFormatError) Error() string {
	return "geoparser: unsupported output format " + e.Format + " (v1 is produced by cachesaver.ConvertV2ToV1 from a v2 cache)"
}

func (f *GeoGen) buildZones() []cachemodel.Zone {
	zones := make([]cachemodel.Zone, 0, len(f.regions)+len(f.countries))
	for _, zone := range f.regions {
		zones = append(zones, cachemodel.Zone{
			Type:    cachemodel.ZoneRegion,
			Name:    unique.Make(zone.Name),
			Bounds:  zone.Bounds,
			Polygon: zone.Polygon,
		})
	}
	for _, zone := range f.countries {
		zones = append(zones, cachemodel.Zone{
			Type:    cachemodel.ZoneCountry,
			Name:    unique.Make(zone.Name),
			Bounds:  zone.Bounds,
			Polygon: zone.Polygon,
		})
	}
	return zones
}
