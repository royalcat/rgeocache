//! Reader for the optional road graph section appended to a v2 cache.
//!
//! Layout (little-endian), positioned directly after the point KDBH block:
//!
//! ```text
//! "RGGR"           [4]byte
//! version          u32 (1)
//! flags            u32 (bit 0: direction metadata present)
//! edge_count       u64
//! max_half_extent  f64 (degrees, candidate-expansion bound for the midpoint index)
//! total_size       u64 (bytes of the whole section)
//! KDBH block over edge midpoints, payload = 18-byte edge record:
//!   from_pos u32 | to_pos u32 | street_id u32 | name_id u32 | class u8 | oneway u8
//! ```
//!
//! Edge endpoints are point positions in the point KDBH sorted order, so
//! coordinates resolve through [`CacheFile::read_coord`] and string ids through
//! [`CacheFile::read_string`]. The section is invisible to readers that predate
//! it because they stop at the end of the point index.

use std::sync::Arc;

use crate::cache::CacheFile;

const GRAPH_MAGIC: &[u8; 4] = b"RGGR";
const GRAPH_VERSION: u32 = 1;
const GRAPH_HEADER_SIZE: usize = 36;
const GRAPH_EDGE_RECORD_SIZE: usize = 18;

const KDBH_MAGIC: &[u8; 4] = b"KDBH";
const KDBH_VERSION: u32 = 1;
const KDBH_HEADER_SIZE: usize = 32;

/// Axis-aligned query box in degrees: x is longitude, y is latitude.
#[derive(Clone, Copy, Debug)]
pub struct BBox {
    pub min_lon: f64,
    pub min_lat: f64,
    pub max_lon: f64,
    pub max_lat: f64,
}

/// A graph edge decoded from the graph section.
#[derive(Clone, Copy, Debug)]
pub struct GraphEdge {
    /// Point position of the edge start in the point KDBH sorted order.
    pub from_pos: u32,
    /// Point position of the edge end.
    pub to_pos: u32,
    /// String id of the street name in the cache string table.
    pub street_id: u32,
    /// String id of the object name in the cache string table.
    pub name_id: u32,
    /// Highway class (see [`class_name`]).
    pub class: u8,
    /// Direction metadata (see [`direction_name`]).
    pub oneway: u8,
}

/// A graph edge matched by a bounding-box query.
#[derive(Clone, Copy, Debug)]
pub struct EdgeHit {
    pub edge_index: u64,
    pub edge: GraphEdge,
    pub from: (f64, f64),
    pub to: (f64, f64),
}

/// An open road graph section backed by the cache mmap.
pub struct RoadGraph {
    cache: Arc<CacheFile>,
    edge_count: usize,
    max_half_extent: f64,
    flags: u32,
    node_size: usize,
    idxs_offset: usize,
    coords_offset: usize,
    data_offsets_offset: usize,
    data_blobs_offset: usize,
}

impl RoadGraph {
    /// Parses the trailing graph section of `cache`.
    ///
    /// Returns `Ok(None)` when the cache predates the graph section or carries
    /// an unknown section version. A recognized but malformed section is an
    /// error: it means the cache itself is inconsistent.
    pub fn load(cache: Arc<CacheFile>) -> Result<Option<Self>, String> {
        let bytes = cache.bytes();
        let start = cache.kdbh_end();
        if start >= bytes.len() {
            return Ok(None);
        }

        let trailing = bytes.len() - start;
        if trailing < GRAPH_HEADER_SIZE {
            log::warn!("ignoring {trailing} trailing bytes after the point index");
            return Ok(None);
        }
        if &bytes[start..start + 4] != GRAPH_MAGIC {
            log::warn!("ignoring trailing cache data: not a road graph section");
            return Ok(None);
        }

        let version = read_u32(bytes, start + 4);
        if version != GRAPH_VERSION {
            log::warn!("ignoring road graph section with unsupported version {version}");
            return Ok(None);
        }

        let flags = read_u32(bytes, start + 8);
        let edge_count = read_u64(bytes, start + 12);
        let max_half_extent = read_f64(bytes, start + 20);
        let total_size = read_u64(bytes, start + 28) as usize;
        if total_size != trailing {
            return Err(format!(
                "graph section size mismatch: header says {total_size} bytes, file has {trailing}"
            ));
        }
        if !max_half_extent.is_finite() || max_half_extent < 0.0 {
            return Err(format!("invalid graph max half extent {max_half_extent}"));
        }
        let edge_count = usize::try_from(edge_count)
            .map_err(|_| format!("graph edge count {edge_count} does not fit in memory"))?;

        let index_base = start + GRAPH_HEADER_SIZE;
        if trailing < GRAPH_HEADER_SIZE + KDBH_HEADER_SIZE {
            return Err("graph section is missing its edge index".into());
        }
        if &bytes[index_base..index_base + 4] != KDBH_MAGIC {
            return Err("graph edge index has invalid magic".into());
        }
        if read_u32(bytes, index_base + 4) != KDBH_VERSION {
            return Err("graph edge index has an unsupported version".into());
        }
        let node_size = read_i64(bytes, index_base + 8);
        let index_count = read_i64(bytes, index_base + 16);
        if node_size < 0 || index_count < 0 || index_count as u64 != edge_count as u64 {
            return Err("graph edge index header does not match the section header".into());
        }
        let node_size = node_size as usize;

        let idxs_offset = index_base + KDBH_HEADER_SIZE;
        let coords_offset = idxs_offset + edge_count * 8;
        let data_offsets_offset = coords_offset + edge_count * 16;
        let data_blobs_offset = data_offsets_offset + (edge_count + 1) * 8;
        if data_blobs_offset > bytes.len() {
            return Err("graph edge index runs past the end of the file".into());
        }

        Ok(Some(Self {
            cache,
            edge_count,
            max_half_extent,
            flags,
            node_size,
            idxs_offset,
            coords_offset,
            data_offsets_offset,
            data_blobs_offset,
        }))
    }

    pub fn edge_count(&self) -> usize {
        self.edge_count
    }

    pub fn max_half_extent(&self) -> f64 {
        self.max_half_extent
    }

    /// Whether the graph carries direction (oneway) metadata.
    pub fn has_directions(&self) -> bool {
        self.flags & 1 != 0
    }

    /// Resolves a cache string id (edge street/name) to its value.
    pub fn string(&self, id: u32) -> String {
        self.cache.read_string(id)
    }

    /// Coordinates (lon, lat) of a point at the given point-index position.
    pub fn coord(&self, pos: u32) -> Option<(f64, f64)> {
        if pos as usize >= self.cache.num_points {
            return None;
        }
        let (x, y) = self.cache.read_coord(pos as usize);
        Some((x.get(), y.get()))
    }

    /// Collects edges whose segment intersects `bbox`, returning at most
    /// `limit` hits and whether more matches were dropped.
    ///
    /// The midpoint index is queried with the box expanded by
    /// [`Self::max_half_extent`]; each candidate is then tested exactly.
    pub fn edges_in_bbox(&self, bbox: &BBox, limit: usize) -> (Vec<EdgeHit>, bool) {
        let mut hits: Vec<EdgeHit> = Vec::new();
        if self.edge_count == 0 || limit == 0 {
            return (hits, false);
        }

        let search = BBox {
            min_lon: bbox.min_lon - self.max_half_extent,
            min_lat: bbox.min_lat - self.max_half_extent,
            max_lon: bbox.max_lon + self.max_half_extent,
            max_lat: bbox.max_lat + self.max_half_extent,
        };

        self.for_each_candidate(&search, |orig| {
            let Some(edge) = self.edge_at(orig as usize) else {
                return true;
            };
            let (Some(from), Some(to)) = (self.coord(edge.from_pos), self.coord(edge.to_pos))
            else {
                return true;
            };
            if !segment_intersects_rect(from.0, from.1, to.0, to.1, bbox) {
                return true;
            }
            hits.push(EdgeHit {
                edge_index: orig,
                edge,
                from,
                to,
            });
            // One extra hit is enough to report truncation.
            hits.len() <= limit
        });

        let truncated = hits.len() > limit;
        hits.truncate(limit);
        (hits, truncated)
    }

    /// Traverses the edge midpoint index, calling `visit` with edge indices
    /// whose midpoint lies in `search`. Stops early when `visit` returns false.
    fn for_each_candidate(&self, search: &BBox, mut visit: impl FnMut(u64) -> bool) {
        let bytes = self.cache.bytes();
        let mut stack: Vec<(i64, i64, u8)> = Vec::with_capacity(64);
        stack.push((0, self.edge_count as i64 - 1, 0));

        while let Some((left, right, axis)) = stack.pop() {
            if left > right {
                continue;
            }
            let (left_u, right_u) = (left as usize, right as usize);

            if right_u - left_u <= self.node_size {
                for i in left_u..=right_u {
                    let (x, y) = self.edge_coord(i);
                    if x >= search.min_lon
                        && x <= search.max_lon
                        && y >= search.min_lat
                        && y <= search.max_lat
                    {
                        let orig = read_i64(bytes, self.idxs_offset + i * 8);
                        if orig >= 0 && !visit(orig as u64) {
                            return;
                        }
                    }
                }
                continue;
            }

            let m = ((left + right) as f64 / 2.0).floor() as i64;
            let (x, y) = self.edge_coord(m as usize);

            if x >= search.min_lon
                && x <= search.max_lon
                && y >= search.min_lat
                && y <= search.max_lat
            {
                let orig = read_i64(bytes, self.idxs_offset + m as usize * 8);
                if orig >= 0 && !visit(orig as u64) {
                    return;
                }
            }

            let next_axis = (axis + 1) % 2;
            let (coord_val, min, max) = if axis == 0 {
                (x, search.min_lon, search.max_lon)
            } else {
                (y, search.min_lat, search.max_lat)
            };
            if min <= coord_val {
                stack.push((left, m - 1, next_axis));
            }
            if max >= coord_val {
                stack.push((m + 1, right, next_axis));
            }
        }
    }

    /// Midpoint coordinates of the edge at graph index position `i`.
    fn edge_coord(&self, i: usize) -> (f64, f64) {
        let bytes = self.cache.bytes();
        let offset = self.coords_offset + i * 16;
        (read_f64(bytes, offset), read_f64(bytes, offset + 8))
    }

    /// Decodes the edge payload at original edge index `orig`.
    fn edge_at(&self, orig: usize) -> Option<GraphEdge> {
        if orig >= self.edge_count {
            return None;
        }
        let bytes = self.cache.bytes();
        let offsets = self.data_offsets_offset + orig * 8;
        let start = read_i64(bytes, offsets);
        let end = read_i64(bytes, offsets + 8);
        if start < 0 || end < start || (end - start) < GRAPH_EDGE_RECORD_SIZE as i64 {
            return None;
        }
        let start = self.data_blobs_offset.checked_add(start as usize)?;
        let end = start.checked_add(GRAPH_EDGE_RECORD_SIZE)?;
        if end > bytes.len() {
            return None;
        }
        let record = &bytes[start..end];
        Some(GraphEdge {
            from_pos: read_u32(record, 0),
            to_pos: read_u32(record, 4),
            street_id: read_u32(record, 8),
            name_id: read_u32(record, 12),
            class: record[16],
            oneway: record[17],
        })
    }
}

/// Highway class name stored in the graph section.
pub fn class_name(class: u8) -> &'static str {
    match class {
        1 => "motorway",
        2 => "trunk",
        3 => "primary",
        4 => "secondary",
        5 => "tertiary",
        _ => "unknown",
    }
}

/// Direction metadata name: travel from `from_pos` to `to_pos`, the reverse, or
/// both.
pub fn direction_name(oneway: u8) -> &'static str {
    match oneway {
        1 => "forward",
        2 => "backward",
        _ => "both",
    }
}

/// Exact segment/rectangle intersection (Liang–Barsky clipping).
fn segment_intersects_rect(x1: f64, y1: f64, x2: f64, y2: f64, rect: &BBox) -> bool {
    let dx = x2 - x1;
    let dy = y2 - y1;
    let mut t0 = 0.0f64;
    let mut t1 = 1.0f64;

    for (p, q) in [
        (-dx, x1 - rect.min_lon),
        (dx, rect.max_lon - x1),
        (-dy, y1 - rect.min_lat),
        (dy, rect.max_lat - y1),
    ] {
        if p == 0.0 {
            if q < 0.0 {
                return false;
            }
            continue;
        }
        let r = q / p;
        if p < 0.0 {
            if r > t1 {
                return false;
            }
            if r > t0 {
                t0 = r;
            }
        } else {
            if r < t0 {
                return false;
            }
            if r < t1 {
                t1 = r;
            }
        }
    }

    true
}

#[inline]
fn read_u32(bytes: &[u8], offset: usize) -> u32 {
    u32::from_le_bytes(bytes[offset..offset + 4].try_into().unwrap())
}

#[inline]
fn read_u64(bytes: &[u8], offset: usize) -> u64 {
    u64::from_le_bytes(bytes[offset..offset + 8].try_into().unwrap())
}

#[inline]
fn read_i64(bytes: &[u8], offset: usize) -> i64 {
    i64::from_le_bytes(bytes[offset..offset + 8].try_into().unwrap())
}

#[inline]
fn read_f64(bytes: &[u8], offset: usize) -> f64 {
    f64::from_bits(read_u64(bytes, offset))
}

#[cfg(test)]
mod tests {
    use super::*;
    use buffa::Message;

    /// Writes a KDBH block whose entries all fit in a single leaf, so the
    /// sorted order equals the insertion order.
    fn push_kdbh(entries: &[(f64, f64, Vec<u8>)], out: &mut Vec<u8>) {
        let n = entries.len();
        const NODE_SIZE: i64 = 64;
        out.extend_from_slice(KDBH_MAGIC);
        out.extend_from_slice(&KDBH_VERSION.to_le_bytes());
        out.extend_from_slice(&NODE_SIZE.to_le_bytes());
        out.extend_from_slice(&(n as i64).to_le_bytes());
        out.extend_from_slice(&[0u8; 8]);
        for i in 0..n {
            out.extend_from_slice(&(i as i64).to_le_bytes());
        }
        for (x, y, _) in entries {
            out.extend_from_slice(&x.to_le_bytes());
            out.extend_from_slice(&y.to_le_bytes());
        }
        let mut offset = 0i64;
        for (_, _, payload) in entries {
            out.extend_from_slice(&offset.to_le_bytes());
            offset += payload.len() as i64;
        }
        out.extend_from_slice(&offset.to_le_bytes());
        for (_, _, payload) in entries {
            out.extend_from_slice(payload);
        }
    }

    fn edge_payload(
        from: u32,
        to: u32,
        street_id: u32,
        name_id: u32,
        class: u8,
        oneway: u8,
    ) -> Vec<u8> {
        let mut payload = Vec::with_capacity(GRAPH_EDGE_RECORD_SIZE);
        payload.extend_from_slice(&from.to_le_bytes());
        payload.extend_from_slice(&to.to_le_bytes());
        payload.extend_from_slice(&street_id.to_le_bytes());
        payload.extend_from_slice(&name_id.to_le_bytes());
        payload.push(class);
        payload.push(oneway);
        payload
    }

    /// Builds a complete v2 cache file with the given points (lon, lat) and
    /// edges (from, to, class, oneway, street_id, name_id). When `graph` is
    /// false the trailing section is omitted.
    fn build_cache(
        points: &[(f64, f64)],
        edges: &[(u32, u32, u8, u8, u32, u32)],
        graph: bool,
    ) -> Vec<u8> {
        let metadata = crate::proto::cache_v1::CacheMetadata {
            version: 2,
            date_created: "2024-01-01T00:00:00Z".into(),
            locale: "en".into(),
            ..Default::default()
        };
        let metadata_bytes = metadata.encode_to_vec();
        let zones_bytes = crate::proto::cache_v2::V2ZonesSection::default().encode_to_vec();
        let header = crate::proto::cache_v2::V2Header {
            metadata_size: metadata_bytes.len() as u32,
            strings_index_size: 0,
            strings_data_size: 0,
            zones_size: zones_bytes.len() as u32,
            ..Default::default()
        };
        let header_bytes = header.encode_to_vec();

        let mut out = Vec::new();
        out.extend_from_slice(b"RGEO");
        out.extend_from_slice(&2u32.to_le_bytes());
        out.extend_from_slice(&(header_bytes.len() as u32).to_le_bytes());
        out.extend_from_slice(&header_bytes);
        out.extend_from_slice(&metadata_bytes);
        out.extend_from_slice(&zones_bytes);

        let point_entries: Vec<(f64, f64, Vec<u8>)> = points
            .iter()
            .map(|(lon, lat)| (*lon, *lat, vec![0u8; 22]))
            .collect();
        push_kdbh(&point_entries, &mut out);

        if graph {
            let edge_entries: Vec<(f64, f64, Vec<u8>)> = edges
                .iter()
                .map(|&(from, to, class, oneway, street_id, name_id)| {
                    let (x1, y1) = points[from as usize];
                    let (x2, y2) = points[to as usize];
                    (
                        (x1 + x2) / 2.0,
                        (y1 + y2) / 2.0,
                        edge_payload(from, to, street_id, name_id, class, oneway),
                    )
                })
                .collect();

            let max_half_extent = edges
                .iter()
                .map(|&(from, to, _, _, _, _)| {
                    let (x1, y1) = points[from as usize];
                    let (x2, y2) = points[to as usize];
                    0.5 * ((x2 - x1).powi(2) + (y2 - y1).powi(2)).sqrt()
                })
                .fold(0.0f64, f64::max);

            let mut index = Vec::new();
            push_kdbh(&edge_entries, &mut index);

            let total_size = (GRAPH_HEADER_SIZE + index.len()) as u64;
            out.extend_from_slice(GRAPH_MAGIC);
            out.extend_from_slice(&GRAPH_VERSION.to_le_bytes());
            out.extend_from_slice(&1u32.to_le_bytes());
            out.extend_from_slice(&(edges.len() as u64).to_le_bytes());
            out.extend_from_slice(&max_half_extent.to_le_bytes());
            out.extend_from_slice(&total_size.to_le_bytes());
            out.extend_from_slice(&index);
        }

        out
    }

    fn open_graph(bytes: &[u8]) -> Result<Option<RoadGraph>, String> {
        let dir = tempfile::tempdir().expect("tempdir");
        let path = dir.path().join("road-graph-test.rgc");
        std::fs::write(&path, bytes).expect("write cache");
        let cache = Arc::new(CacheFile::open(path.to_str().unwrap()).expect("open cache"));
        RoadGraph::load(cache)
    }

    #[test]
    fn loads_and_filters_edges() {
        // Four nodes on a line plus a detached pair far away.
        let points = [
            (0.0, 0.0),
            (0.001, 0.0),
            (0.002, 0.0),
            (0.003, 0.0),
            (10.0, 10.0),
            (10.001, 10.0),
        ];
        let edges = [
            (0, 1, 1, 1, 0, 0),
            (1, 2, 3, 0, 0, 0),
            (2, 3, 5, 2, 0, 0),
            (4, 5, 2, 0, 0, 0),
        ];
        let bytes = build_cache(&points, &edges, true);
        let graph = open_graph(&bytes).expect("load").expect("graph section");

        assert_eq!(graph.edge_count(), 4);
        assert!(graph.has_directions());

        // The first three edges intersect the small box; the far edge does not.
        let bbox = BBox {
            min_lon: -0.0005,
            min_lat: -0.0005,
            max_lon: 0.0035,
            max_lat: 0.0005,
        };
        let (hits, truncated) = graph.edges_in_bbox(&bbox, 100);
        assert!(!truncated);
        assert_eq!(hits.len(), 3);

        let first = hits.iter().find(|hit| hit.edge_index == 0).expect("edge 0");
        assert_eq!(first.edge.class, 1);
        assert_eq!(first.edge.oneway, 1);
        assert_eq!(first.from, (0.0, 0.0));
        assert_eq!(first.to, (0.001, 0.0));
        assert_eq!(direction_name(first.edge.oneway), "forward");

        let third = hits.iter().find(|hit| hit.edge_index == 2).expect("edge 2");
        assert_eq!(direction_name(third.edge.oneway), "backward");
        assert_eq!(class_name(third.edge.class), "tertiary");
    }

    #[test]
    fn truncates_at_limit() {
        let points = [(0.0, 0.0), (0.001, 0.0), (0.002, 0.0), (0.003, 0.0)];
        let edges = [(0, 1, 1, 0, 0, 0), (1, 2, 1, 0, 0, 0), (2, 3, 1, 0, 0, 0)];
        let bytes = build_cache(&points, &edges, true);
        let graph = open_graph(&bytes).expect("load").expect("graph section");

        let bbox = BBox {
            min_lon: -1.0,
            min_lat: -1.0,
            max_lon: 1.0,
            max_lat: 1.0,
        };
        let (hits, truncated) = graph.edges_in_bbox(&bbox, 2);
        assert!(truncated);
        assert_eq!(hits.len(), 2);
    }

    #[test]
    fn empty_box_returns_nothing() {
        let points = [(0.0, 0.0), (0.001, 0.0)];
        let edges = [(0, 1, 1, 0, 0, 0)];
        let bytes = build_cache(&points, &edges, true);
        let graph = open_graph(&bytes).expect("load").expect("graph section");

        let bbox = BBox {
            min_lon: 50.0,
            min_lat: 50.0,
            max_lon: 51.0,
            max_lat: 51.0,
        };
        let (hits, truncated) = graph.edges_in_bbox(&bbox, 100);
        assert!(hits.is_empty());
        assert!(!truncated);
    }

    #[test]
    fn missing_section_is_none() {
        let points = [(0.0, 0.0)];
        let bytes = build_cache(&points, &[], false);
        assert!(open_graph(&bytes).expect("load").is_none());
    }

    #[test]
    fn unsupported_version_is_none() {
        let points = [(0.0, 0.0)];
        let mut bytes = build_cache(&points, &[], false);
        bytes.extend_from_slice(GRAPH_MAGIC);
        bytes.extend_from_slice(&7u32.to_le_bytes());
        bytes.extend_from_slice(&[0u8; GRAPH_HEADER_SIZE - 8]);
        assert!(open_graph(&bytes).expect("load").is_none());
    }

    #[test]
    fn size_mismatch_is_error() {
        let points = [(0.0, 0.0), (0.001, 0.0)];
        let edges = [(0, 1, 1, 0, 0, 0)];
        let mut bytes = build_cache(&points, &edges, true);
        bytes.push(0); // trailing byte the section header does not account for
        assert!(open_graph(&bytes).is_err());
    }
}
