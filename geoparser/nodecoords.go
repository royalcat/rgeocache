package geoparser

import (
	"slices"
	"sync"

	"github.com/paulmach/osm"
	"github.com/royalcat/osmpbfdb"
)

// resolvedNode is the location of one way node; ok is false when the node
// could not be placed (and the failure was logged).
type resolvedNode struct {
	lon, lat float64
	ok       bool
}

// nodeScratch holds the buffers one resolveNodes call needs. They are pooled
// because a way is resolved millions of times per run.
type nodeScratch struct {
	out    []resolvedNode
	ids    []osm.NodeID
	idx    []int
	coords []osmpbfdb.NodeCoord
}

var nodeScratchPool = sync.Pool{New: func() any { return new(nodeScratch) }}

func getNodeScratch() *nodeScratch  { return nodeScratchPool.Get().(*nodeScratch) }
func putNodeScratch(s *nodeScratch) { nodeScratchPool.Put(s) }

// resolveNodes returns the location of every node of a way, in order. Nodes
// that carry their own coordinates (PBFs with locations on ways) are used as
// they are; the rest are looked up in the node database in one batch, which
// lets the database serve consecutive nodes from the same decoded block.
//
// The result aliases s and is valid until s is reused or returned to the pool.
func (f *GeoGen) resolveNodes(nodes osm.WayNodes, s *nodeScratch) []resolvedNode {
	out := slices.Grow(s.out[:0], len(nodes))[:len(nodes)]
	ids, idx := s.ids[:0], s.idx[:0]

	for i, n := range nodes {
		if n.Lat != 0 && n.Lon != 0 {
			out[i] = resolvedNode{lon: n.Lon, lat: n.Lat, ok: true}
			continue
		}
		out[i] = resolvedNode{}
		ids = append(ids, n.ID)
		idx = append(idx, i)
	}

	if len(ids) > 0 {
		if resolver, ok := f.osmdb.(osmpbfdb.NodeCoordResolver); ok {
			s.coords = slices.Grow(s.coords[:0], len(ids))[:len(ids)]
			err := resolver.GetNodeCoords(ids, s.coords)
			for j, c := range s.coords {
				switch {
				case !c.Found:
					cause := osmpbfdb.ErrNotFound
					if err != nil {
						cause = err
					}
					f.log.Error("failed to get node", "id", ids[j], "error", cause.Error())
				case c.Lat == 0 && c.Lon == 0:
					f.log.Error("node has no coordinates", "id", ids[j])
				default:
					out[idx[j]] = resolvedNode{lon: c.Lon, lat: c.Lat, ok: true}
				}
			}
		} else {
			for j, id := range ids {
				p, err := f.osmdb.GetNode(id)
				switch {
				case err != nil:
					f.log.Error("failed to get node", "id", id, "error", err.Error())
				case p.Lat == 0 && p.Lon == 0:
					f.log.Error("node has no coordinates", "id", id)
				default:
					out[idx[j]] = resolvedNode{lon: p.Lon, lat: p.Lat, ok: true}
				}
			}
		}
	}

	s.out, s.ids, s.idx = out, ids, idx
	return out
}
