package geoparser

import (
	"context"
	"log/slog"
	"runtime"
	"sync"

	"github.com/paulmach/osm"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/royalcat/osmpbfdb"
	"github.com/royalcat/rgeocache/bordertree"
	"go.opentelemetry.io/otel"
)

var tracer = otel.Tracer("github.com/royalcat/rgeocache/geoparser")

type GeoGen struct {
	config Config

	placeIndex  *bordertree.BorderTree[string]
	regionIndex *bordertree.BorderTree[string]

	localizationCache *xsync.MapOf[string, string]

	osmdb osmpbfdb.OsmDB

	parsedPointsMu sync.Mutex
	parsedPoints   []geoPoint

	parsedNodes     *set[osm.NodeID]
	parsedWays      *set[osm.WayID]
	parsedRelations *set[osm.RelationID]

	log *slog.Logger
}

func NewGeoGen(db osmpbfdb.OsmDB, config Config) (*GeoGen, error) {
	return &GeoGen{
		placeIndex:        bordertree.NewBorderTree[string](),
		regionIndex:       bordertree.NewBorderTree[string](),
		localizationCache: xsync.NewMapOf[string, string](),

		parsedNodes:     newSet[osm.NodeID](),
		parsedWays:      newSet[osm.WayID](),
		parsedRelations: newSet[osm.RelationID](),

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

func (f *GeoGen) ParseOSMData(ctx context.Context) error {
	ctx, span := tracer.Start(ctx, "ParseOSMData")
	defer span.End()

	err := f.fillRelCache(ctx, f.osmdb)
	if err != nil {
		return err
	}

	err = f.parseDatabase(ctx, f.osmdb)
	if err != nil {
		return err
	}

	return nil
}
