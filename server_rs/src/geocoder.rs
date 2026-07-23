//! Disk-backed reverse geocoder.
//!
//! Uses the mmap'd KD-tree spatial index for radius search, resolves string
//! IDs lazily from the string data block, and falls back to border trees
//! for region/country when a point is not found or is missing those fields.

use multiversion::multiversion;
use vecpool::PoolVec;

use crate::border_tree::BorderTree;
use crate::cache::{CacheFile, V2PointData};

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

/// Disk-backed reverse geocoder.
pub struct Geocoder {
    cache: CacheFile,
    regions: BorderTree,
    countries: BorderTree,
    search_radius: f64,
}

impl Geocoder {
    /// Load a v2 cache file and build border trees.
    pub fn load(path: &str, search_radius: f64) -> Result<Self, Box<dyn std::error::Error>> {
        let cache = CacheFile::open(path)?;

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

    /// Find the closest address within the given radius (degrees).
    pub fn find_in_radius(&self, lat: f64, lon: f64, radius: f64) -> Option<Info> {
        if self.cache.num_points == 0 {
            return self.border_fallback(lat, lon);
        }

        let cache = &self.cache;

        let mut best_point: Option<V2PointData> = None;
        let mut best_dist: f64 = f64::INFINITY;
        let r2 = radius * radius;

        // Stack-based KD-tree traversal (ported from kdbush_disk.go)
        let mut stack: PoolVec<(i64, i64, u8)> = vecpool::with_capacity(64);
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
                    let x = coords[i * 2];
                    let y = coords[i * 2 + 1];
                    let dist = sq_dist(x, y, lon, lat);
                    if dist <= r2 {
                        let data = cache.read_point_data(idxs[i] as usize);
                        if dist < best_dist
                            || data.weight > best_point.map(|p| p.weight).unwrap_or(0)
                        {
                            best_dist = dist;
                            best_point = Some(data);
                        }
                    }
                }
                continue;
            }

            // Internal node
            let m = ((left + right) as f64 / 2.0).floor() as usize;
            let (x, y) = cache.read_coord(m);

            let dist = sq_dist(x, y, lon, lat);
            if dist <= r2 {
                let idx = cache.read_idx(m) as usize;
                let data = cache.read_point_data(idx);
                if dist < best_dist || data.weight > best_point.map(|p| p.weight).unwrap_or(0) {
                    best_dist = dist;
                    best_point = Some(data);
                }
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

        // --- Resolve the best match ---
        if let Some(data) = best_point {
            let mut info = resolve(&cache, data);

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
#[multiversion(targets("x86_64+avx512f", "aarch64+neon"))]
fn sq_dist(ax: f64, ay: f64, bx: f64, by: f64) -> f64 {
    let dx = ax - bx;
    let dy = ay - by;
    dx * dx + dy * dy
}
