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
//
// Batches arrive in input order, so the string ids it assigns, the order of the
// records, the synthetic node keys and the order of the zones it collects are
// all deterministic. Zones are only collected on the main stream (isMain).
func (f *GeoGen) collectWorker(items <-chan parseItem, sp *spool.Writer, table *strtab.Table, isMain bool) (err error) {
	defer func() {
		if err != nil {
			// The parser may still be writing to the channel. Drain it so it
			// cannot block forever after a collection failure.
			go func() {
				for range items {
				}
			}()
		}
	}()

	var synthSeq uint64
	warned := false
	for item := range items {
		if item.Region != nil {
			f.regions = append(f.regions, *item.Region)
		}
		if item.Country != nil {
			f.countries = append(f.countries, *item.Country)
		}

		occurrences := int64(sp.NumPoints()) + int64(len(item.Points))
		if occurrences > int64(math.MaxUint32) {
			return fmt.Errorf("geoparser: point occurrences exceed the uint32 cache limit (%d); split the input into smaller regions", math.MaxUint32)
		}
		if isMain && !warned && occurrences > int64(math.MaxUint32)/10*9 {
			warned = true
			f.log.Warn("Point occurrences passed 90% of the uint32 cache limit", "occurrences", occurrences)
		}
		base := uint32(sp.NumPoints())

		for _, p := range item.Points {
			nodeKey := p.NodeKey
			if nodeKey == syntheticNodeKey {
				// Interpolated points get a key that is unique across the run
				// and assigned in input order.
				synthSeq++
				nodeKey = syntheticNodeKey | synthSeq
			}
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
				NodeKey:       nodeKey,
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

		if table.DataSize() > strtab.MaxDataSize {
			return fmt.Errorf("geoparser: string table exceeds the 4 GiB v2 limit (%d bytes); split the input into smaller regions", table.DataSize())
		}
	}
	return nil
}

// appendNodePoints appends the address-node points, collected into their own
// spool and string table, to the main spool. Their string ids are translated
// to the main table in the node table's id order, which is deterministic.
//
// Node points carry no edges and no OSM node key, so placing them after every
// way and relation point leaves all edge occurrence ids unchanged.
func appendNodePoints(spoolDir string, mainTable *strtab.Table, nodeSp *spool.Reader, nodeTable *strtab.Table) error {
	if nodeSp.NumPoints() == 0 {
		return nil
	}

	mainSp, err := spool.Open(spoolDir)
	if err != nil {
		return err
	}
	if total := int64(mainSp.NumPoints()) + int64(nodeSp.NumPoints()); total > int64(math.MaxUint32) {
		return fmt.Errorf("geoparser: point occurrences exceed the uint32 cache limit (%d); split the input into smaller regions", math.MaxUint32)
	}

	remap := make([]uint32, nodeTable.Len())
	for id := 1; id < nodeTable.Len(); id++ {
		remap[id] = mainTable.Add(nodeTable.String(uint32(id)))
	}
	if mainTable.DataSize() > strtab.MaxDataSize {
		return fmt.Errorf("geoparser: string table exceeds the 4 GiB v2 limit (%d bytes); split the input into smaller regions", mainTable.DataSize())
	}

	_, err = spool.AppendPoints(spoolDir, nodeSp, func(p *spool.Point) {
		p.NameID = remap[p.NameID]
		p.StreetID = remap[p.StreetID]
		p.HouseNumberID = remap[p.HouseNumberID]
		p.CityID = remap[p.CityID]
		p.RegionID = remap[p.RegionID]
	})
	return err
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
