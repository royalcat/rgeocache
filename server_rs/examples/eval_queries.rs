//! Manual evaluation harness for forward geocoding.
//!
//! Builds (or reuses) the forward geocoder index for a cache and prints the
//! ranked results for a set of queries, so ranking and recall changes can be
//! compared on the real distribution. Not used by tests or CI.
//!
//! ```sh
//! cargo run --release --example eval_queries -- \
//!     bin/points_data_v2.rgc /tmp/fg-index лени "тверск 12"
//! ```
//!
//! The second argument is optional; without it the index is built into a
//! temporary directory and thrown away. With a directory the persisted index is
//! reused across runs, which is what makes before/after comparisons cheap.

use std::path::PathBuf;
use std::sync::Arc;

use rgeocache_server::cache::CacheFile;
use rgeocache_server::forward_geocoder::{ForwardGeocoder, SearchRequest};
use rgeocache_server::geocoder::Geocoder;

const DEFAULT_QUERIES: &[&str] = &[
    "лени",
    "ленина",
    "тверская",
    "тверск 12",
    "москва",
    "советская",
    "садовая",
    "королёв",
    "королев",
    "12 к 1",
];

fn main() -> Result<(), Box<dyn std::error::Error>> {
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("info")).init();

    let mut args = std::env::args().skip(1);
    let cache_path = args
        .next()
        .ok_or("usage: eval_queries <cache.rgc> [index_dir] [query...]")?;

    // Any further argument that names an existing directory (or ends with a
    // separator) is the index directory; everything else is a query.
    let mut index_dir: Option<PathBuf> = None;
    let mut queries: Vec<String> = Vec::new();
    for arg in args {
        let path = PathBuf::from(&arg);
        if index_dir.is_none() && (path.is_dir() || arg.ends_with('/')) {
            index_dir = Some(path);
        } else {
            queries.push(arg);
        }
    }
    if queries.is_empty() {
        queries = DEFAULT_QUERIES.iter().map(|q| q.to_string()).collect();
    }

    let cache = Arc::new(CacheFile::open(&cache_path)?);
    eprintln!(
        "cache: {} points, locale={:?}",
        cache.num_points, cache.locale
    );
    let geocoder = Arc::new(Geocoder::load(cache, 0.01)?);

    let started = std::time::Instant::now();
    let (forward, mode) = ForwardGeocoder::build(geocoder, index_dir.as_deref())?;
    eprintln!("index: {mode:?} in {:.1}s", started.elapsed().as_secs_f64());

    for query in &queries {
        println!("=== {query:?}");
        let request = SearchRequest {
            query: Some(query.clone()),
            ..Default::default()
        };
        let results = forward.search(&request)?;
        if results.is_empty() {
            println!("    (no results)");
        }
        for item in results {
            println!(
                "    {:>12.2}  {:<8}  {:<24}  {}",
                item.score, item.geo_type, item.country, item.address_string
            );
        }
        let suggestions = forward.suggest(query, 4)?;
        if !suggestions.is_empty() {
            let rendered: Vec<String> = suggestions
                .iter()
                .map(|s| format!("{} ({})", s.text, s.doc_freq))
                .collect();
            println!("    suggestions: {}", rendered.join(", "));
        }
    }

    Ok(())
}
