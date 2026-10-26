//! Border tree: spatial index for region/country polygon lookups.
//!
//! Uses an rstar R-tree for bounding-box filtering, then exact point-in-polygon
//! containment via the `geo` crate. Read-only after construction.

use geo::{Area, Contains};
use multiversion::multiversion;
use rstar::{PointDistance, RStarInsertionStrategy, RTree, RTreeObject, RTreeParams, AABB};

use crate::cache::ZoneType;

// ---------------------------------------------------------------------------
// R-tree entry: a polygon with its bounding box and associated name
// ---------------------------------------------------------------------------

#[derive(Clone, Debug)]
struct ZoneEntry {
    name: String,
    polygon: geo::MultiPolygon<f64>,
    area: f64,
    // oversimplified polygon for faster point-in-polygon checks
    // oversimplified_polygon: geo::MultiPolygon<f64>,
    envelope: AABB<[f64; 2]>,
}

impl RTreeObject for ZoneEntry {
    type Envelope = AABB<[f64; 2]>;

    #[inline]
    fn envelope(&self) -> Self::Envelope {
        self.envelope
    }
}

impl PointDistance for ZoneEntry {
    #[inline]
    fn distance_2(&self, point: &[f64; 2]) -> f64 {
        self.envelope.distance_2(point)
    }
}

// ---------------------------------------------------------------------------
// BorderTree
// ---------------------------------------------------------------------------

struct BorderRTreeNodeParameters;

impl RTreeParams for BorderRTreeNodeParameters {
    const MIN_SIZE: usize = 2;
    const MAX_SIZE: usize = 6;
    const REINSERTION_COUNT: usize = 3;
    type DefaultInsertionStrategy = RStarInsertionStrategy;
}

type BorderRTree = RTree<ZoneEntry, BorderRTreeNodeParameters>;

/// Spatial index for zone polygons (regions or countries).
pub struct BorderTree {
    tree: BorderRTree,
    smallest: bool,
}

// const OVERSIMPLIFIED_BORDER_EPSILON: f64 = 1_000_000.0;

impl BorderTree {
    /// Build a border tree from a set of zones of the given type.
    ///
    /// `smallest` selects the smallest containing polygon instead of the first
    /// match. Use it for regions and places, where overlapping boundaries
    /// (for example former provinces and current regions) must resolve
    /// deterministically; keep `false` for countries.
    pub fn build(
        zones: &[crate::cache::IndexedZone],
        filter_type: ZoneType,
        smallest: bool,
    ) -> Self {
        let entries: Vec<ZoneEntry> = zones
            .iter()
            .filter(|z| z.zone_type == filter_type)
            .map(|z| {
                let bbox = polygon_bbox(&z.polygon);
                ZoneEntry {
                    name: z.name.clone(),
                    area: z.polygon.unsigned_area(),
                    polygon: z.polygon.clone(),
                    // oversimplified_polygon: z
                    //     .polygon
                    //     .clone()
                    //     .simplify_vw_preserve(OVERSIMPLIFIED_BORDER_EPSILON),
                    envelope: AABB::from_corners(
                        [bbox.min().x, bbox.min().y],
                        [bbox.max().x, bbox.max().y],
                    ),
                }
            })
            .collect();

        Self {
            tree: BorderRTree::bulk_load_with_params(entries),
            smallest,
        }
    }

    /// Query for the name of the polygon containing the given point.
    /// x = longitude, y = latitude.
    pub fn query_point(&self, x: f64, y: f64) -> Option<&str> {
        let point = [x, y];
        let geo_point: geo::Point = geo::Coord { x, y }.into();

        // Find all candidates whose bounding box contains the point.
        let mut best: Option<&ZoneEntry> = None;
        for entry in self.tree.locate_all_at_point(point) {
            // if multipolygon_contains(&entry.oversimplified_polygon, &geo_point) {
            //     continue;
            // }
            if !multipolygon_contains(&entry.polygon, &geo_point) {
                continue;
            }

            if !self.smallest {
                return Some(&entry.name);
            }

            best = match best {
                None => Some(entry),
                Some(current)
                    if entry.area < current.area
                        || (entry.area == current.area && entry.name < current.name) =>
                {
                    Some(entry)
                }
                Some(current) => Some(current),
            };
        }
        best.map(|entry| entry.name.as_str())
    }
}

#[inline(always)]
#[multiversion(targets = "simd")]
fn multipolygon_contains(polygon: &geo::MultiPolygon<f64>, point: &geo::Point) -> bool {
    return polygon.contains(point);
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
    bbox.unwrap_or_else(|| {
        geo::Rect::new(geo::Coord { x: 0.0, y: 0.0 }, geo::Coord { x: 0.0, y: 0.0 })
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cache::{IndexedZone, ZoneType};

    fn square(min_x: f64, min_y: f64, max_x: f64, max_y: f64) -> geo::MultiPolygon<f64> {
        geo::MultiPolygon(vec![geo::Polygon::new(
            geo::LineString::from(vec![
                (min_x, min_y),
                (max_x, min_y),
                (max_x, max_y),
                (min_x, max_y),
                (min_x, min_y),
            ]),
            vec![],
        )])
    }

    fn zone(name: &str, min_x: f64, min_y: f64, max_x: f64, max_y: f64) -> IndexedZone {
        IndexedZone {
            name: name.to_string(),
            zone_type: ZoneType::Region,
            polygon: square(min_x, min_y, max_x, max_y),
            centroid: geo::Point::new(0.0, 0.0),
        }
    }

    #[test]
    fn smallest_region_is_insertion_order_independent() {
        let large = zone("province", 0.0, 0.0, 2.0, 2.0);
        let small = zone("region", 0.5, 0.5, 1.5, 1.5);

        for zones in [
            vec![large.clone(), small.clone()],
            vec![small.clone(), large.clone()],
        ] {
            let tree = BorderTree::build(&zones, ZoneType::Region, true);
            assert_eq!(tree.query_point(1.0, 1.0), Some("region"));
            assert_eq!(tree.query_point(0.25, 0.25), Some("province"));
        }
    }

    #[test]
    fn smallest_region_breaks_equal_area_ties_by_name() {
        let left = zone("a", 0.0, 0.0, 2.0, 1.0);
        let right = zone("b", 1.0, 0.0, 3.0, 1.0);

        for zones in [
            vec![left.clone(), right.clone()],
            vec![right.clone(), left.clone()],
        ] {
            let tree = BorderTree::build(&zones, ZoneType::Region, true);
            assert_eq!(tree.query_point(1.5, 0.5), Some("a"));
        }
    }

    #[test]
    fn first_match_is_kept_for_countries() {
        let mut country = zone("country", 0.0, 0.0, 2.0, 2.0);
        country.zone_type = ZoneType::Country;
        let zones = vec![country];
        let tree = BorderTree::build(&zones, ZoneType::Country, false);
        assert_eq!(tree.query_point(1.0, 1.0), Some("country"));
        assert_eq!(tree.query_point(3.0, 3.0), None);
    }
}
