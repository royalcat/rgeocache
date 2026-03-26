package geoparser

import (
	"io"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/paulmach/osm"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/bordertree"
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/internal/rangeindex"
	"golang.org/x/sync/errgroup"
)

type GeoGen struct {
	osmdb  osmpbfdb.OsmDB
	config Config

	placeIndex  *bordertree.BorderTree[string]
	regionIndex *bordertree.BorderTree[string]

	localizationCache *xsync.MapOf[string, string]

	parsedNodes          *rangeindex.Index[osm.NodeID, struct{}]
	parsedNodesDupes     atomic.Uint64
	parsedWays           *rangeindex.Index[osm.WayID, struct{}]
	parsedWaysDupes      atomic.Uint64
	parsedRelations      *rangeindex.Index[osm.RelationID, struct{}]
	parsedRelationsDupes atomic.Uint64

	parsedPoints chan geoPoint
	parsingDone  chan struct{}

	regionsMu sync.Mutex
	regions   []geomodel.Zone

	countriesMu sync.Mutex
	countries   []geomodel.Zone

	output io.Writer

	log *slog.Logger
}

func NewGeoGen(db osmpbfdb.OsmDB, config Config, output io.Writer) (*GeoGen, error) {
	return &GeoGen{
		osmdb:  db,
		config: config,

		placeIndex:        bordertree.NewBorderTree[string](),
		regionIndex:       bordertree.NewBorderTree[string](),
		localizationCache: xsync.NewMapOf[string, string](),

		parsedNodes:     rangeindex.New[osm.NodeID, struct{}](),
		parsedWays:      rangeindex.New[osm.WayID, struct{}](),
		parsedRelations: rangeindex.New[osm.RelationID, struct{}](),

		regions:   []geomodel.Zone{},
		countries: []geomodel.Zone{},

		output: output,

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
	f.parsedPoints = make(chan geoPoint, 10)
	f.parsingDone = make(chan struct{})

	var wg errgroup.Group
	wg.Go(f.saveWorker)
	wg.Go(func() error {
		err := f.fillRelCache()
		if err != nil {
			return err
		}

		err = f.parseDatabase()
		if err != nil {
			return err
		}

		close(f.parsedPoints)
		close(f.parsingDone)

		return nil
	})

	return wg.Wait()
}
