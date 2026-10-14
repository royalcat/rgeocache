//! Disk-backed reverse geocoder.
//!
//! Uses the mmap'd KD-tree spatial index for radius search, resolves string
//! IDs lazily from the string data block, and falls back to border trees
//! for region/country when a point is not found or is missing those fields.
use std::cmp::Ordering;
use std::collections::BinaryHeap;
use std::sync::Arc;

use crate::border_tree::BorderTree;
use crate::cache::{CacheFile, V2PointData};
use crate::forward_geocoder::{GeoObjectKind, GeocodeKindFilter};
use multiversion::multiversion;
use smallvec::SmallVec;

/// JSON-serializable reverse geocode result.
#[derive(serde::Serialize, Clone, Debug)]
pub struct Info {
    pub name: String,
    pub street: String,
    pub house_number: String,
    pub city: String,
    pub region: String,
    pub country: String,
    pub weight: u8,
}

/// One cached point returned by [`Geocoder::points_in_radius`].
#[derive(Debug, Clone)]
pub struct PointInRadius {
    /// Longitude in degrees.
    pub lon: f64,
    /// Latitude in degrees.
    pub lat: f64,
    /// The point's address; `region` and `country` are filled from the border
    /// trees when the stored record left them empty, like [`Geocoder::find`].
    pub info: Info,
    /// Object kind, from the cache's explicit type byte with a weight fallback.
    pub kind: GeoObjectKind,
}

/// Nearest points within a radius, with the total number of matches.
#[derive(Debug, Default)]
pub struct PointsInRadius {
    /// Up to the requested limit, nearest first.
    pub points: Vec<PointInRadius>,
    /// Total number of matching points; exceeds `points.len()` when truncated.
    pub total: usize,
}

/// Disk-backed reverse geocoder.
pub struct Geocoder {
    pub cache: Arc<CacheFile>,
    regions: BorderTree,
    countries: BorderTree,
    search_radius: f64,
}

impl Geocoder {
    /// Load a v2 cache file and build border trees.
    pub fn load(
        cache: Arc<CacheFile>,
        search_radius: f64,
    ) -> Result<Self, Box<dyn std::error::Error>> {
        let regions = BorderTree::build(&cache.zones, crate::cache::ZoneType::Region);
        let countries = BorderTree::build(&cache.zones, crate::cache::ZoneType::Country);

        log::info!(
            "Loaded v2 cache: {} points, {} zones, node_size={}",
            cache.num_points,
            cache.zones.len(),
            cache.node_size
        );

        Ok(Self {
            cache,
            regions,
            countries,
            search_radius,
        })
    }

    /// Find the closest address for the given coordinates.
    /// Returns `None` if nothing is found within the search radius.
    pub fn find(&self, lat: f64, lon: f64) -> Option<Info> {
        self.find_in_radius(lat, lon, self.search_radius)
    }

    /// Country name containing the given point (x=lon, y=lat), if any.
    ///
    /// Used by the forward geocoder to attach a country to every indexed point;
    /// the reverse path performs the same lookup inside [`Self::find_in_radius`].
    pub fn country_at(&self, lon: f64, lat: f64) -> Option<&str> {
        self.countries.query_point(lon, lat)
    }

    /// Find the closest address within the given radius (degrees).
    pub fn find_in_radius(&self, lat: f64, lon: f64, radius: f64) -> Option<Info> {
        if self.cache.num_points == 0 {
            return self.border_fallback(lat, lon);
        }

        let cache = &self.cache;

        let best_point = tree_find_in_radius(cache, lon, lat, radius);

        // --- Resolve the best match ---
        if let Some(data) = best_point {
            let mut info = resolve(cache, data);

            // Fallback region/country if point didn't have them
            let pt_x = lon;
            let pt_y = lat;
            if info.region.is_empty() {
                if let Some(region) = self.regions.query_point(pt_x, pt_y) {
                    info.region = region.to_string();
                }
            }
            if info.country.is_empty() {
                if let Some(country) = self.countries.query_point(pt_x, pt_y) {
                    info.country = country.to_string();
                }
            }

            return Some(info);
        }

        // --- No point within radius: try borders alone ---
        self.border_fallback(lat, lon)
    }

    /// Return up to `limit` cached points within `radius` degrees of (lat, lon),
    /// nearest first, along with the total number of matches.
    ///
    /// The kind filter selects object kinds; a cached point is never `zone`, so
    /// a zones-only filter matches nothing.
    pub fn points_in_radius(
        &self,
        lat: f64,
        lon: f64,
        radius: f64,
        limit: usize,
        filter: &GeocodeKindFilter,
    ) -> PointsInRadius {
        if self.cache.num_points == 0 {
            return PointsInRadius::default();
        }

        let mut collector = NearestCollector::new(limit);
        for_each_in_radius(&self.cache, lon, lat, radius, |x, y, data| {
            let kind = GeoObjectKind::from_cache(data.geo_type, data.weight);
            if !filter.matches(kind) {
                return;
            }
            collector.add(x, y, sq_dist(x, y, lon, lat), kind, data);
        });

        let mut points = Vec::with_capacity(collector.len());
        let total = collector.total;
        for hit in collector.into_sorted() {
            let mut info = resolve(&self.cache, hit.data);
            if info.region.is_empty() {
                if let Some(region) = self.regions.query_point(hit.lon, hit.lat) {
                    info.region = region.to_string();
                }
            }
            if info.country.is_empty() {
                if let Some(country) = self.countries.query_point(hit.lon, hit.lat) {
                    info.country = country.to_string();
                }
            }
            points.push(PointInRadius {
                lon: hit.lon,
                lat: hit.lat,
                info,
                kind: hit.kind,
            });
        }

        PointsInRadius { points, total }
    }

    /// Fallback: region/country from border trees only (no point match).
    fn border_fallback(&self, lat: f64, lon: f64) -> Option<Info> {
        let country = self.countries.query_point(lon, lat).map(|s| s.to_string());
        let region = self.regions.query_point(lon, lat).map(|s| s.to_string());

        if country.is_some() || region.is_some() {
            Some(Info {
                name: String::new(),
                street: String::new(),
                house_number: String::new(),
                city: String::new(),
                region: region.unwrap_or_default(),
                country: country.unwrap_or_default(),
                weight: 0,
            })
        } else {
            None
        }
    }
}

/// Visit every cache point within `radius` degrees of (lon, lat) — `x` is
/// longitude, `y` is latitude. This is the stack-based KD-tree traversal
/// (ported from kdbush_disk.go) that the reverse lookup and the points listing
/// share.
fn for_each_in_radius(
    cache: &CacheFile,
    lon: f64,
    lat: f64,
    radius: f64,
    mut visit: impl FnMut(f64, f64, V2PointData),
) {
    let r2 = radius * radius;

    // Stack-based KD-tree traversal (ported from kdbush_disk.go)
    let mut stack: SmallVec<(i64, i64, u8), 64> = SmallVec::with_capacity(64);
    stack.push((0, cache.num_points as i64 - 1, 0));

    while let Some((left, right, axis)) = stack.pop() {
        if left > right {
            continue;
        }

        let left_u = left as usize;
        let right_u = right as usize;

        // Leaf node: linear scan
        if right_u - left_u <= cache.node_size {
            let (idxs, coords) = cache.read_leaf(left_u, right_u);
            for i in 0..idxs.len() {
                let x = coords[i * 2].get();
                let y = coords[i * 2 + 1].get();
                if sq_dist(x, y, lon, lat) <= r2 {
                    visit(x, y, cache.read_point_data(idxs[i].get() as usize));
                }
            }
            continue;
        }

        // Internal node
        let m = ((left + right) as f64 / 2.0).floor() as usize;
        let (x, y) = cache.read_coord(m);
        let (x, y) = (x.get(), y.get());

        if sq_dist(x, y, lon, lat) <= r2 {
            let idx = cache.read_idx(m).get() as usize;
            visit(x, y, cache.read_point_data(idx));
        }

        let next_axis = (axis + 1) % 2;

        // Decide which children to visit
        let cmp = if axis == 0 {
            lon - radius
        } else {
            lat - radius
        };
        let coord_val = if axis == 0 { x } else { y };

        if cmp <= coord_val {
            stack.push((left, m as i64 - 1, next_axis));
        }
        let cmp = if axis == 0 {
            lon + radius
        } else {
            lat + radius
        };
        if cmp >= coord_val {
            stack.push((m as i64 + 1, right, next_axis));
        }
    }
}

/// Nearest point within `radius` degrees of (lon, lat).
fn tree_find_in_radius(cache: &CacheFile, lon: f64, lat: f64, radius: f64) -> Option<V2PointData> {
    let mut best_point: Option<V2PointData> = None;
    let mut best_dist: f64 = f64::INFINITY;

    for_each_in_radius(cache, lon, lat, radius, |x, y, data| {
        let dist = sq_dist(x, y, lon, lat);
        if dist < best_dist || data.weight > best_point.map(|p| p.weight).unwrap_or(0) {
            best_dist = dist;
            best_point = Some(data);
        }
    });

    best_point
}

/// Keeps the nearest `limit` hits seen during a radius traversal with bounded
/// memory, while counting every match so callers can detect truncation.
struct NearestCollector {
    limit: usize,
    total: usize,
    /// Max-heap on distance²: the farthest kept hit is at the root.
    hits: BinaryHeap<NearestHit>,
}

struct NearestHit {
    dist2: f64,
    lon: f64,
    lat: f64,
    kind: GeoObjectKind,
    data: V2PointData,
}

impl PartialEq for NearestHit {
    fn eq(&self, other: &Self) -> bool {
        self.dist2 == other.dist2
    }
}

impl Eq for NearestHit {}

impl PartialOrd for NearestHit {
    fn partial_cmp(&self, other: &Self) -> Option<Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for NearestHit {
    fn cmp(&self, other: &Self) -> Ordering {
        // BinaryHeap is a max-heap, so the farthest hit must compare greatest.
        self.dist2.total_cmp(&other.dist2)
    }
}

impl NearestCollector {
    fn new(limit: usize) -> Self {
        Self {
            limit,
            total: 0,
            hits: BinaryHeap::new(),
        }
    }

    fn add(&mut self, lon: f64, lat: f64, dist2: f64, kind: GeoObjectKind, data: V2PointData) {
        self.total += 1;
        if self.limit == 0 {
            return;
        }

        let hit = NearestHit {
            dist2,
            lon,
            lat,
            kind,
            data,
        };
        if self.hits.len() < self.limit {
            self.hits.push(hit);
            return;
        }
        if hit.dist2 < self.hits.peek().map(|h| h.dist2).unwrap_or(f64::INFINITY) {
            self.hits.pop();
            self.hits.push(hit);
        }
    }

    fn len(&self) -> usize {
        self.hits.len()
    }

    /// Drain the collector nearest-first.
    fn into_sorted(self) -> Vec<NearestHit> {
        let mut hits = self.hits.into_vec();
        hits.sort_by(|a, b| a.dist2.total_cmp(&b.dist2));
        hits
    }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/// Resolve string IDs from V2PointData using the cache's string table.
fn resolve(cache: &CacheFile, data: V2PointData) -> Info {
    Info {
        name: cache.read_string(data.name_id.get()),
        street: cache.read_string(data.street_id.get()),
        house_number: cache.read_string(data.house_number_id.get()),
        city: cache.read_string(data.city_id.get()),
        region: cache.read_string(data.region_id.get()),
        country: String::new(), // country only from border trees
        weight: data.weight,
    }
}

/// Squared distance between two points (x=lon, y=lat).
#[inline]
#[multiversion(targets = "simd")]
fn sq_dist(ax: f64, ay: f64, bx: f64, by: f64) -> f64 {
    let dx = ax - bx;
    let dy = ay - by;
    dx * dx + dy * dy
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::test_cache::{build_cache, open_cache, point_payload, TestZone};

    /// Cache with three points around the origin and one far away:
    ///
    /// - `(0.001, 0.001)` building, "Main 7", "Town"
    /// - `(-0.002, 0)`    road, "Ring Road"
    /// - `(0.006, 0)`     area
    /// - `(10, 10)`       building, far outside any test radius
    fn test_geocoder(search_radius: f64) -> Geocoder {
        // String ids: 0 = "", 1 = "Main", 2 = "7", 3 = "Town", 4 = "Ring Road"
        let strings = ["", "Main", "7", "Town", "Ring Road"];
        let points = [
            (0.001, 0.001, point_payload(0, 1, 2, 3, 0, 10, 1)),
            (-0.002, 0.0, point_payload(0, 4, 0, 0, 0, 5, 2)),
            (0.006, 0.0, point_payload(0, 0, 0, 0, 0, 3, 3)),
            (10.0, 10.0, point_payload(0, 0, 0, 0, 0, 10, 1)),
        ];
        let ring = vec![
            (-1.0, -1.0),
            (1.0, -1.0),
            (1.0, 1.0),
            (-1.0, 1.0),
            (-1.0, -1.0),
        ];
        let zones = [
            TestZone {
                zone_type: 1,
                name: "Test Region",
                ring: ring.clone(),
            },
            TestZone {
                zone_type: 2,
                name: "Testland",
                ring,
            },
        ];
        Geocoder::load(
            open_cache(&build_cache(&points, &strings, &zones)),
            search_radius,
        )
        .expect("load geocoder")
    }

    #[test]
    fn finds_points_nearest_first_with_border_fallback() {
        let geocoder = test_geocoder(0.01);
        let listed = geocoder.points_in_radius(0.0, 0.0, 0.01, 100, &GeocodeKindFilter::default());

        assert_eq!(listed.total, 3);
        assert_eq!(listed.points.len(), 3);

        let streets: Vec<&str> = listed
            .points
            .iter()
            .map(|point| point.info.street.as_str())
            .collect();
        assert_eq!(streets, ["Main", "Ring Road", ""], "nearest-first order");

        let nearest = &listed.points[0];
        assert_eq!(nearest.info.house_number, "7");
        assert_eq!(nearest.info.city, "Town");
        assert_eq!(nearest.info.region, "Test Region", "region border fallback");
        assert_eq!(nearest.info.country, "Testland", "country border fallback");
        assert_eq!(nearest.kind, GeoObjectKind::Building);
        assert!((nearest.lon - 0.001).abs() < f64::EPSILON);
        assert!((nearest.lat - 0.001).abs() < f64::EPSILON);

        let kinds: Vec<GeoObjectKind> = listed.points.iter().map(|point| point.kind).collect();
        assert_eq!(
            kinds,
            [
                GeoObjectKind::Building,
                GeoObjectKind::Road,
                GeoObjectKind::Area,
            ]
        );
    }

    #[test]
    fn limits_to_nearest_and_reports_total() {
        let geocoder = test_geocoder(0.01);
        let listed = geocoder.points_in_radius(0.0, 0.0, 0.01, 2, &GeocodeKindFilter::default());

        assert_eq!(listed.total, 3, "total counts every match");
        assert_eq!(listed.points.len(), 2, "limit caps the returned points");
        assert_eq!(listed.points[0].info.street, "Main");
        assert_eq!(listed.points[1].info.street, "Ring Road");
    }

    #[test]
    fn filters_by_kind() {
        let geocoder = test_geocoder(0.01);

        let roads =
            geocoder.points_in_radius(0.0, 0.0, 0.01, 100, &GeocodeKindFilter::from("road"));
        assert_eq!(roads.total, 1);
        assert_eq!(roads.points.len(), 1);
        assert_eq!(roads.points[0].info.street, "Ring Road");

        // A cache point is never a zone, so a zones-only filter matches nothing.
        let zones =
            geocoder.points_in_radius(0.0, 0.0, 0.01, 100, &GeocodeKindFilter::from("zone"));
        assert_eq!(zones.total, 0);
        assert!(zones.points.is_empty());
    }

    #[test]
    fn excludes_points_outside_the_radius() {
        let geocoder = test_geocoder(0.01);
        let listed =
            geocoder.points_in_radius(0.0, 0.0, 0.0015, 100, &GeocodeKindFilter::default());

        assert_eq!(listed.total, 1);
        assert_eq!(listed.points[0].info.street, "Main");
    }

    #[test]
    fn legacy_record_falls_back_to_weight_for_kind() {
        // A 21-byte record (no geo type byte) is widened with geo_type == 0,
        // so the kind must come from the weight.
        let strings = ["", "Ring Road"];
        let payload = {
            let mut payload = point_payload(0, 1, 0, 0, 0, 5, 0);
            payload.pop();
            payload
        };
        let geocoder = Geocoder::load(
            open_cache(&build_cache(&[(0.0, 0.0, payload)], &strings, &[])),
            0.01,
        )
        .expect("load geocoder");

        let listed = geocoder.points_in_radius(0.0, 0.0, 0.01, 10, &GeocodeKindFilter::default());
        assert_eq!(listed.total, 1);
        assert_eq!(listed.points[0].kind, GeoObjectKind::Road);
    }
}
