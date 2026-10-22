package geoparser

import (
	"fmt"
	"io"
	"math"

	"github.com/royalcat/rgeocache/cachesaver"
	cachemodel "github.com/royalcat/rgeocache/cachesaver/model"
	"github.com/royalcat/rgeocache/internal/spool"
	"github.com/royalcat/rgeocache/internal/strtab"
)

// collectWorker turns parse batches into spool records. Point occurrence ids
// are the point indexes in the spool, which is what edges reference.
func (f *GeoGen) collectWorker(sp *spool.Writer, table *strtab.Table) (err error) {
	defer func() {
		if err != nil {
			// The parser may still be writing to the channel. Drain it so it
			// cannot block forever after a collection failure.
			go func() {
				for range f.parsedItems {
				}
			}()
		}
	}()

	for item := range f.parsedItems {
		if int64(sp.NumPoints())+int64(len(item.Points)) > int64(math.MaxUint32) {
			return fmt.Errorf("geoparser: point occurrences exceed the uint32 cache limit")
		}
		base := uint32(sp.NumPoints())

		for _, p := range item.Points {
			rec := spool.Point{
				X:             p.X(),
				Y:             p.Y(),
				NameID:        table.Add(p.Name),
				StreetID:      table.Add(p.Street.Value()),
				HouseNumberID: table.Add(p.HouseNumber.Value()),
				CityID:        table.Add(p.City.Value()),
				RegionID:      table.Add(p.Region.Value()),
				Weight:        p.Weight,
				GeoType:       uint8(p.Type),
				NodeKey:       p.NodeKey,
				ClaimSeq:      p.ClaimSeq,
			}
			if err := sp.WritePoint(rec); err != nil {
				return err
			}
		}

		for _, e := range item.Edges {
			if e.FromLocal >= uint32(len(item.Points)) || e.ToLocal >= uint32(len(item.Points)) {
				return fmt.Errorf(
					"geoparser: edge endpoint out of range: %d -> %d (batch has %d points)",
					e.FromLocal, e.ToLocal, len(item.Points),
				)
			}
			rec := spool.Edge{
				From:     base + e.FromLocal,
				To:       base + e.ToLocal,
				StreetID: table.Add(e.Street.Value()),
				NameID:   table.Add(e.Name.Value()),
				Class:    e.Class,
				Oneway:   e.Oneway,
			}
			if err := sp.WriteEdge(rec); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeV2Output(
	sp *spool.Reader,
	table *strtab.Table,
	zones []cachemodel.Zone,
	meta cachemodel.Metadata,
	w io.Writer,
	workDir string,
) error {
	return cachesaver.SaveV2FromSpool(sp, table, zones, meta, w, workDir)
}
