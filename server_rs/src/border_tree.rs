//! Border tree: spatial index for region/country polygon lookups.
//!
//! Uses an rstar R-tree for bounding-box filtering, then exact point-in-polygon
//! containment via the `geo` crate. Read-only after construction.

use geo::Contains;
use rstar::{PointDistance, RTree, RTreeObject, AABB};

use crate::cache::ZoneType;

// ---------------------------------------------------------------------------
// R-tree entry: a polygon with its bounding box and associated name
// ---------------------------------------------------------------------------

#[derive(Clone, Debug)]
struct ZoneEntry {
    name: String,
    polygon: geo::MultiPolygon<f64>,
    envelope: AABB<[f64; 2]>,
}

impl RTreeObject for ZoneEntry {
    type Envelope = AABB<[f64; 2]>;

    fn envelope(&self) -> Self::Envelope {
        self.envelope
    }
}

impl PointDistance for ZoneEntry {
    fn distance_2(&self, point: &[f64; 2]) -> f64 {
        self.envelope.distance_2(point)
    }
}

// ---------------------------------------------------------------------------
// BorderTree
// ---------------------------------------------------------------------------

/// Spatial index for zone polygons (regions or countries).
pub struct BorderTree {
    tree: RTree<ZoneEntry>,
}

impl BorderTree {
    /// Build a border tree from a set of zones of the given type.
    pub fn build(zones: &[crate::cache::IndexedZone], filter_type: ZoneType) -> Self {
        let entries: Vec<ZoneEntry> = zones
            .iter()
            .filter(|z| z.zone_type == filter_type)
            .map(|z| {
                let bbox = polygon_bbox(&z.polygon);
                ZoneEntry {
                    name: z.name.clone(),
                    polygon: z.polygon.clone(),
                    envelope: AABB::from_corners(
                        [bbox.min().x, bbox.min().y],
                        [bbox.max().x, bbox.max().y],
                    ),
                }
            })
            .collect();

        Self {
            tree: RTree::bulk_load(entries),
        }
    }

    /// Query for the name of the polygon containing the given point.
    /// x = longitude, y = latitude.
    pub fn query_point(&self, x: f64, y: f64) -> Option<&str> {
        let point = [x, y];
        let geo_point: geo::Point = geo::Coord { x, y }.into();

        // Find all candidates whose bounding box contains the point
        for entry in self.tree.locate_all_at_point(&point) {
            if entry.polygon.contains(&geo_point) {
                return Some(&entry.name);
            }
        }
        None
    }
}

/// Compute the bounding box of a MultiPolygon.
fn polygon_bbox(mp: &geo::MultiPolygon<f64>) -> geo::Rect<f64> {
    let mut bbox: Option<geo::Rect<f64>> = None;
    for poly in &mp.0 {
        for coord in poly.exterior().coords() {
            match &mut bbox {
                Some(b) => {
                    let mut min = b.min();
                    let mut max = b.max();
                    if coord.x < min.x {
                        min.x = coord.x;
                    }
                    if coord.y < min.y {
                        min.y = coord.y;
                    }
                    if coord.x > max.x {
                        max.x = coord.x;
                    }
                    if coord.y > max.y {
                        max.y = coord.y;
                    }
                    *b = geo::Rect::new(min, max);
                }
                None => {
                    bbox = Some(geo::Rect::new(*coord, *coord));
                }
            }
        }
    }
    bbox.unwrap_or_else(|| geo::Rect::new(geo::Coord { x: 0.0, y: 0.0 }, geo::Coord { x: 0.0, y: 0.0 }))
}
