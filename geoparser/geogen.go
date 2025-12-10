package geoparser

import (
	"log/slog"
	"runtime"
	"sync"

	"github.com/puzpuzpuz/xsync/v3"
	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/bordertree"
)

type GeoGen struct {
	config Config

	placeIndex  *bordertree.BorderTree[string]
	regionIndex *bordertree.BorderTree[string]

	localizationCache *xsync.MapOf[string, string]

	osmdb osmpbfdb.OsmDB

	parsedPointsMu sync.Mutex
	parsedPoints   []geoPoint

	log *slog.Logger
}

func NewGeoGen(db osmpbfdb.OsmDB, config Config) (*GeoGen, error) {
	return &GeoGen{
		placeIndex:        bordertree.NewBorderTree[string](),
		regionIndex:       bordertree.NewBorderTree[string](),
		localizationCache: xsync.NewMapOf[string, string](),

		config: config,

		osmdb: db,

		parsedPoints: []geoPoint{},

		log: slog.Default(),
	}, nil
}

func (f *GeoGen) ResetCache() error {
	f.placeIndex = bordertree.NewBorderTree[string]()
	f.regionIndex = bordertree.NewBorderTree[string]()
	f.localizationCache = xsync.NewMapOf[string, string]()
	runtime.GC()

	return nil
}

func (f *GeoGen) ParseOSMData() error {
	err := f.fillRelCache(f.osmdb)
	if err != nil {
		return err
	}

	err = f.parseDatabase(f.osmdb)
	if err != nil {
		return err
	}

	return nil
}
