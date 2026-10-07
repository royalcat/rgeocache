//! rgeocache server: mmap-only reverse geocoding, forward geocoding and road
//! graph queries.
//!
//! The binary in `main.rs` is a thin wrapper around this library so that
//! examples, benchmarks and tests can drive the cache readers and the forward
//! geocoder directly.

#[allow(unused_imports, dead_code)]
pub mod proto;

pub mod border_tree;
pub mod cache;
pub mod forward_geocoder;
pub mod geocoder;
pub mod road_graph;
pub mod server;
