//! V2 cache file loader (mmap-only).
//!
//! The v2 cache format:
//!
//! ```text
//! [0..4)   Magic "RGEO"
//! [4..8)   Compat level u32 LE (must be 2)
//! [8..12)  V2Header protobuf size u32 LE
//! [12..H)  V2Header protobuf
//! [H..)    CacheMetadata protobuf (metadata_size bytes)
//! ...      String offset index (strings_index_size bytes, N × u32 LE)
//! ...      String data block (strings_data_size bytes, null-terminated)
//! ...      V2ZonesSection protobuf (zones_size bytes)
//! ...      KDBH binary block (32-byte header + tree + data)
//! ```
//!
//! KDBH binary block (after the 32-byte header):
//!
//! ```text
//! Tree section (N = num_points):
//!   [H      .. H+N*8)   idxs     N × i64 LE   (sorted original indices)
//!   [H+N*8  .. H+N*24)  coords   N × 2 × f64 LE (sorted x,y pairs)
//!
//! Data section:
//!   [D      .. D+(N+1)*8)   offsets  (N+1) × i64 LE  (cumulative byte offsets)
//!   [B      .. EOF)         blobs    concatenated 21-byte V2PointData records
//! ```

use memmap2::Mmap;
use prost::Message;
use zerocopy::byteorder::little_endian::{I64 as I64LE, U32 as U32LE};
use zerocopy::{FromBytes, Immutable, IntoBytes, KnownLayout};

use crate::proto;

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

pub const MAGIC: &[u8; 4] = b"RGEO";
pub const COMPAT_LEVEL_V2: u32 = 2;
const KDBH_MAGIC: &[u8; 4] = b"KDBH";
const KDBH_VERSION: u32 = 1;
const KDBH_HEADER_SIZE: usize = 32;
pub const V2_POINT_DATA_SIZE: usize = 21;

// ---------------------------------------------------------------------------
// V2PointData — on-disk point payload (21 bytes, little-endian)
// ---------------------------------------------------------------------------

/// On-disk point data: 5× u32 string IDs (LE) + weight.
/// ID 0 means empty string.
///
/// Derives `FromBytes` + `IntoBytes` for zero-cost transmutation from/to
/// the mmap'd byte region.  `#[repr(C)]` guarantees the exact 21-byte
/// layout with no padding.
#[derive(FromBytes, IntoBytes, KnownLayout, Immutable, Clone, Copy, Debug, Default)]
#[repr(C)]
pub struct V2PointData {
    pub name_id: U32LE,
    pub street_id: U32LE,
    pub house_number_id: U32LE,
    pub city_id: U32LE,
    pub region_id: U32LE,
    pub weight: u8,
}

impl V2PointData {
    /// Create an empty (all-zeros) point data.
    pub fn empty() -> Self {
        Self::default()
    }
}

// ---------------------------------------------------------------------------
// CacheMetadata — parsed manually (simple fields, avoids v1 proto dependency)
// ---------------------------------------------------------------------------

#[derive(Clone, Debug)]
pub struct CacheMetadata {
    #[allow(dead_code)]
    pub version: u32,
    #[allow(dead_code)]
    pub date_created: String,
    #[allow(dead_code)]
    pub locale: String,
}

// ---------------------------------------------------------------------------
// IndexedZone — resolved zone ready for border tree insertion
// ---------------------------------------------------------------------------

#[derive(Clone, Debug)]
pub struct IndexedZone {
    pub name: String,
    pub zone_type: ZoneType,
    pub polygon: geo::MultiPolygon<f64>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ZoneType {
    Region = 1,
    Country = 2,
}

// ---------------------------------------------------------------------------
// CacheFile — mmap'd v2 cache
// ---------------------------------------------------------------------------

/// An open, mmap'd v2 cache file.
///
/// The spatial index stays on disk; only the string offset index and zone data
/// are loaded into memory.
pub struct CacheFile {
    mmap: Mmap,

    // String resolution
    pub strings_index: Vec<u32>, // id → byte offset into string data block
    pub strings_data_offset: usize, // absolute position in the mmap'd file

    // Zone data
    pub zones: Vec<IndexedZone>,

    // KDBH spatial index layout
    pub num_points: usize,
    pub node_size: usize,
    pub idxs_offset: usize,         // absolute position of sorted indices
    pub coords_offset: usize,       // absolute position of sorted coords
    pub data_offsets_offset: usize, // absolute position of cumulative data offsets
    pub data_blobs_offset: usize,   // absolute position of concatenated blobs
}

// Helper: read a U32LE from mmap at the given offset.
#[inline]
fn read_u32le(mmap: &Mmap, offset: usize) -> Result<u32, String> {
    U32LE::read_from_bytes(&mmap[offset..offset + 4])
        .map(|v| v.get())
        .map_err(|e| format!("failed to read u32 at {offset}: {e}"))
}

// Helper: read an I64LE from mmap at the given offset.
#[inline]
fn read_i64le(mmap: &Mmap, offset: usize) -> Result<i64, String> {
    I64LE::read_from_bytes(&mmap[offset..offset + 8])
        .map(|v| v.get())
        .map_err(|e| format!("failed to read i64 at {offset}: {e}"))
}

impl CacheFile {
    /// Open and parse a v2 cache file via mmap.
    pub fn open(path: &str) -> Result<Self, Box<dyn std::error::Error>> {
        let file = std::fs::File::open(path)?;
        let mmap = unsafe { Mmap::map(&file)? };

        mmap.advise(memmap2::Advice::Random)?;

        // should be no-op in worst case, but positive performance gain in good cases. no harm enabling it
        if let Err(err) = mmap.advise(memmap2::Advice::HugePage) {
            log::info!("huge page not avalible: {err}; continuing without it");
        }

        let mut offset: usize = 0;

        // --- Verify magic bytes ---
        let magic = &mmap[offset..offset + 4];
        if magic != MAGIC {
            return Err(format!("invalid magic bytes: {magic:?}").into());
        }
        offset += 4;

        // --- Verify compatibility level ---
        let compat = read_u32le(&mmap, offset)?;
        if compat != COMPAT_LEVEL_V2 {
            return Err(format!(
                "expected v2 cache (compat level {}), got {}",
                COMPAT_LEVEL_V2, compat
            )
            .into());
        }
        offset += 4;

        // --- Read V2Header protobuf ---
        let header_size = read_u32le(&mmap, offset)? as usize;
        offset += 4;

        let header = proto::V2Header::decode(&mmap[offset..offset + header_size])?;
        offset += header_size;

        // --- Read CacheMetadata (manual protobuf decode for the 3-field message) ---
        let metadata_size = header.metadata_size as usize;
        let metadata =
            proto::cache_v1::CacheMetadata::decode(&mmap[offset..offset + metadata_size])?;
        log::info!(
            "cache metadata: version={} date_created={} locale={}",
            metadata.version,
            metadata.date_created,
            metadata.locale
        );
        offset += metadata_size;

        // --- Read string offset index into memory ---
        let strings_index_size = header.strings_index_size as usize;
        let num_strings = strings_index_size / 4;
        let mut strings_index = Vec::with_capacity(num_strings);
        for i in 0..num_strings {
            let base = offset + i * 4;
            strings_index.push(read_u32le(&mmap, base)?);
        }
        offset += strings_index_size;

        // --- Record string data block position (lazy reads) ---
        let strings_data_offset = offset;
        offset += header.strings_data_size as usize;

        // --- Read and parse zones section ---
        let zones_size = header.zones_size as usize;
        let zones_section = proto::V2ZonesSection::decode(&mmap[offset..offset + zones_size])?;
        let zones = parse_zones(&zones_section);
        offset += zones_size;

        // --- Parse KDBH header and pre-compute section offsets ---
        let kdbh_base = offset;

        // Verify KDBH magic
        let kdbh_magic = &mmap[kdbh_base..kdbh_base + 4];
        if kdbh_magic != KDBH_MAGIC {
            return Err(format!("invalid KDBH magic: {kdbh_magic:?}").into());
        }

        let kdbh_version = read_u32le(&mmap, kdbh_base + 4)?;
        if kdbh_version != KDBH_VERSION {
            return Err(format!(
                "unsupported KDBH version {} (want {})",
                kdbh_version, KDBH_VERSION
            )
            .into());
        }

        let node_size = read_i64le(&mmap, kdbh_base + 8)? as usize;
        let num_points = read_i64le(&mmap, kdbh_base + 16)? as usize;

        let idxs_offset = kdbh_base + KDBH_HEADER_SIZE;
        let coords_offset = idxs_offset + num_points * 8;
        let data_offsets_offset = coords_offset + num_points * 16;
        let data_blobs_offset = data_offsets_offset + (num_points + 1) * 8;

        Ok(Self {
            mmap,
            strings_index,
            strings_data_offset,
            zones,
            num_points,
            node_size,
            idxs_offset,
            coords_offset,
            data_offsets_offset,
            data_blobs_offset,
        })
    }

    // --- Low-level mmap reads (used by the KD-tree traversal) ---

    /// Read a single original-index value at sorted position `i`.
    #[inline]
    pub fn read_idx(&self, i: usize) -> i64 {
        let pos = self.idxs_offset + i * 8;
        I64LE::read_from_bytes(&self.mmap[pos..pos + 8])
            .map(|v| v.get())
            .unwrap_or(0)
    }

    /// Read the (x, y) coordinate pair for sorted position `i`.
    /// Coordinates are stored as f64 LE.  We read the bits via I64LE and
    /// transmute to f64 — this is valid because f64 LE has the same byte
    /// layout as a little-endian u64.
    #[inline]
    pub fn read_coord(&self, i: usize) -> (f64, f64) {
        let pos = self.coords_offset + i * 16;
        let x = I64LE::read_from_bytes(&self.mmap[pos..pos + 8])
            .map(|v| f64::from_bits(v.get() as u64))
            .unwrap_or(0.0);
        let y = I64LE::read_from_bytes(&self.mmap[pos + 8..pos + 16])
            .map(|v| f64::from_bits(v.get() as u64))
            .unwrap_or(0.0);
        (x, y)
    }

    /// Batch-read indices and coordinates for leaf range `[left, right]` inclusive.
    ///
    /// Uses zerocopy `read_from_bytes` for each element — cleaner and equivalently
    /// fast compared to manual `from_le_bytes`, since the compiler optimizes both
    /// to direct memory loads on little-endian hardware.
    pub fn read_leaf(&self, left: usize, right: usize) -> (Vec<i64>, Vec<f64>) {
        let count = right - left + 1;
        let mut idxs = Vec::with_capacity(count);
        let mut coords = Vec::with_capacity(count * 2);

        for i in left..=right {
            idxs.push(self.read_idx(i));
            let (x, y) = self.read_coord(i);
            coords.push(x);
            coords.push(y);
        }

        (idxs, coords)
    }

    /// Read the V2PointData blob for the point at original index `orig_idx`.
    /// Uses zerocopy [`FromBytes`] for zero-cost transmutation from the mmap'd bytes.
    #[inline]
    pub fn read_point_data(&self, orig_idx: usize) -> V2PointData {
        // Read offsets[orig_idx] and offsets[orig_idx+1] as two consecutive i64 LE values.
        let off_pos = self.data_offsets_offset + orig_idx * 8;
        let blob_start = read_i64le(&self.mmap, off_pos).unwrap_or(0) as usize;
        let blob_end = read_i64le(&self.mmap, off_pos + 8).unwrap_or(0) as usize;

        let blob_len = blob_end - blob_start;
        if blob_len == 0 {
            return V2PointData::empty();
        }

        let blob_pos = self.data_blobs_offset + blob_start;
        V2PointData::read_from_bytes(&self.mmap[blob_pos..blob_pos + V2_POINT_DATA_SIZE])
            .unwrap_or_else(|_| V2PointData::empty())
    }

    /// Read a null-terminated string from the string data block by its ID.
    /// ID 0 is the empty string.
    #[inline]
    pub fn read_string(&self, id: u32) -> String {
        if id == 0 {
            return String::new();
        }
        let start = self.strings_index[id as usize] as usize;
        let pos = self.strings_data_offset + start;

        // Scan for null terminator (strings are at most ~500 bytes)
        let end = self.mmap[pos..].iter().position(|&b| b == 0).unwrap_or(512);
        String::from_utf8_lossy(&self.mmap[pos..pos + end]).into_owned()
    }
}

/// Convert proto geometry types to geo::MultiPolygon.
fn parse_zones(section: &proto::V2ZonesSection) -> Vec<IndexedZone> {
    let mut zones = Vec::with_capacity(section.blobs.len());

    for blob in &section.blobs {
        let zone_type = match blob.zone_type {
            1 => ZoneType::Region,
            2 => ZoneType::Country,
            _ => continue,
        };

        for zone in &blob.zones {
            let name = String::from_utf8_lossy(&zone.name).into_owned();
            let polygon = convert_multi_polygon(zone.multi_polygon.as_ref());
            zones.push(IndexedZone {
                name,
                zone_type,
                polygon,
            });
        }
    }

    zones.shrink_to_fit();

    zones
}

/// Convert a proto MultiPolygon to geo::MultiPolygon.
fn convert_multi_polygon(mp: Option<&proto::MultiPolygon>) -> geo::MultiPolygon<f64> {
    let mp = match mp {
        Some(m) => m,
        None => return geo::MultiPolygon::new(vec![]),
    };

    let polygons: Vec<geo::Polygon<f64>> = mp
        .polygons
        .iter()
        .map(|p| {
            let rings: Vec<geo::LineString<f64>> = p
                .rings
                .iter()
                .map(|r| {
                    let coords: Vec<geo::Coord<f64>> = r
                        .points
                        .iter()
                        .map(|ll| geo::Coord {
                            x: ll.lon as f64,
                            y: ll.lat as f64,
                        })
                        .collect();
                    geo::LineString::new(coords)
                })
                .collect();

            let exterior = rings
                .first()
                .cloned()
                .unwrap_or_else(|| geo::LineString::new(vec![]));
            let interiors = rings.iter().skip(1).cloned().collect();
            geo::Polygon::new(exterior, interiors)
        })
        .collect();

    geo::MultiPolygon::new(polygons)
}
