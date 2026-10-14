//! Synthetic v2 cache files for in-module tests.
//!
//! Mirrors the layout the Go `cachesaver.SaveV2` writer produces — magic,
//! compat level, `V2Header`, metadata, string table, zones and a KDBH point
//! index. The KDBH entries all fit in a single leaf, so the sorted order
//! equals the insertion order and tests can reason about point positions
//! directly.

use std::sync::Arc;

use buffa::Message;

use crate::cache::CacheFile;

/// One zone written into the synthetic cache's zones section.
pub(crate) struct TestZone<'a> {
    /// 1 = region, 2 = country.
    pub zone_type: u32,
    pub name: &'a str,
    /// Exterior ring as `(lon, lat)` pairs.
    pub ring: Vec<(f64, f64)>,
}

/// Builds the 22-byte point payload from string-table ids, weight and the
/// explicit geo type byte.
pub(crate) fn point_payload(
    name_id: u32,
    street_id: u32,
    house_number_id: u32,
    city_id: u32,
    region_id: u32,
    weight: u8,
    geo_type: u8,
) -> Vec<u8> {
    let mut payload = Vec::with_capacity(22);
    for id in [name_id, street_id, house_number_id, city_id, region_id] {
        payload.extend_from_slice(&id.to_le_bytes());
    }
    payload.push(weight);
    payload.push(geo_type);
    payload
}

/// Writes a KDBH block whose entries all fit in a single leaf, so the sorted
/// order equals the insertion order.
pub(crate) fn push_kdbh(entries: &[(f64, f64, Vec<u8>)], out: &mut Vec<u8>) {
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

/// Same magic + version the cache reader verifies.
const KDBH_MAGIC: &[u8; 4] = b"KDBH";
const KDBH_VERSION: u32 = 1;

/// Builds a complete v2 cache file with the given points, string table and
/// zones.
///
/// `strings` must start with the empty string (id 0); later entries get ids
/// 1, 2, ... A point payload usually comes from [`point_payload`].
pub(crate) fn build_cache(
    points: &[(f64, f64, Vec<u8>)],
    strings: &[&str],
    zones: &[TestZone],
) -> Vec<u8> {
    let metadata = crate::proto::cache_v1::CacheMetadata {
        version: 2,
        date_created: "2024-01-01T00:00:00Z".into(),
        locale: "en".into(),
        ..Default::default()
    };
    let metadata_bytes = metadata.encode_to_vec();

    let (offset_index, string_data) = string_table(strings);
    let zones_bytes = zones_section(zones).encode_to_vec();

    let header = crate::proto::cache_v2::V2Header {
        metadata_size: metadata_bytes.len() as u32,
        strings_index_size: (offset_index.len() * 4) as u32,
        strings_data_size: string_data.len() as u32,
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
    for offset in &offset_index {
        out.extend_from_slice(&offset.to_le_bytes());
    }
    out.extend_from_slice(&string_data);
    out.extend_from_slice(&zones_bytes);

    push_kdbh(points, &mut out);

    out
}

/// Writes a synthetic cache to a temporary file and opens it.
///
/// The file is unlinked while the mmap stays alive, which is fine on the
/// platforms the server targets.
pub(crate) fn open_cache(bytes: &[u8]) -> Arc<CacheFile> {
    let dir = tempfile::tempdir().expect("tempdir");
    let path = dir.path().join("test-cache.rgc");
    std::fs::write(&path, bytes).expect("write cache");
    Arc::new(CacheFile::open(path.to_str().unwrap()).expect("open cache"))
}

/// Offsets and null-terminated data of the string table. Entry 0 is the empty
/// string, matching the Go writer.
fn string_table(strings: &[&str]) -> (Vec<u32>, Vec<u8>) {
    let mut offsets = Vec::with_capacity(strings.len());
    let mut data = Vec::new();
    for string in strings {
        offsets.push(data.len() as u32);
        data.extend_from_slice(string.as_bytes());
        data.push(0);
    }
    (offsets, data)
}

fn zones_section(zones: &[TestZone]) -> crate::proto::cache_v2::V2ZonesSection {
    let mut section = crate::proto::cache_v2::V2ZonesSection::default();
    for zone in zones {
        let ring = crate::proto::cache_v2::Ring {
            points: zone
                .ring
                .iter()
                .map(|&(lon, lat)| crate::proto::cache_v2::LatLon {
                    lat: lat as f32,
                    lon: lon as f32,
                    ..Default::default()
                })
                .collect(),
            ..Default::default()
        };
        let polygon = crate::proto::cache_v2::Polygon {
            rings: vec![ring],
            ..Default::default()
        };
        let multi_polygon = crate::proto::cache_v2::MultiPolygon {
            polygons: vec![polygon],
            ..Default::default()
        };
        let v2zone = crate::proto::cache_v2::V2Zone {
            name: zone.name.to_string(),
            multi_polygon: multi_polygon.into(),
            ..Default::default()
        };
        section.blobs.push(crate::proto::cache_v2::V2ZoneBlob {
            zone_type: zone.zone_type,
            zones: vec![v2zone],
            ..Default::default()
        });
    }
    section
}
