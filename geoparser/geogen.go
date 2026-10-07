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
	"github.com/royalcat/rgeocache/geomodel"
	"github.com/royalcat/rgeocache/internal/bordertree"
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

	// graphNodes maps the OSM node ids of road shape points to dense graph node
	// ids (starting at 1). Each node is emitted as a cache point exactly once,
	// by the way that first claims it, so ways sharing a junction reference the
	// same point.
	graphNodes      *xsync.MapOf[osm.NodeID, uint32]
	graphNodeSeq    atomic.Uint32
	graphNodesDupes atomic.Uint64

	parsedItems chan parseItem
	parsingDone chan struct{}

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

		placeIndex:        bordertree.NewBorderTree[string](),
		regionIndex:       bordertree.NewBorderTree[string](),
		localizationCache: xsync.NewMapOf[string, string](),

		parsedNodes:     rangeindex.New[osm.NodeID, struct{}](),
		parsedWays:      rangeindex.New[osm.WayID, struct{}](),
		parsedRelations: rangeindex.New[osm.RelationID, struct{}](),

		graphNodes: xsync.NewMapOf[osm.NodeID, uint32](),

		regions:   []geomodel.Zone{},
		countries: []geomodel.Zone{},

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

type ParseOutput struct {
	Format string
	Writer io.Writer
}

func (f *GeoGen) ParseOSMData(outputs []ParseOutput) error {
	f.parsedItems = make(chan parseItem, 10)
	f.parsingDone = make(chan struct{})

	var wg errgroup.Group
	wg.Go(func() error {
		return f.saveWorker(outputs)
	})
	wg.Go(func() error {
		err := f.fillRelCache()
		if err != nil {
			return err
		}

		err = f.parseDatabase()
		if err != nil {
			return err
		}

		close(f.parsedItems)
		close(f.parsingDone)

		return nil
	})

	return wg.Wait()
}
