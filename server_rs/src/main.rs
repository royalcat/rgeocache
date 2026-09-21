mod border_tree;
mod cache;
mod forward_geocoder;
mod geocoder;
mod server;

#[allow(unused_imports, dead_code)]
mod proto;

use std::path::PathBuf;
use std::sync::{Arc, OnceLock};
use std::thread;

use clap::Parser;
use ntex::http::HttpServiceConfig;
use ntex::io::IoConfig;
use ntex::web::{HttpServer, WebAppConfig};
use ntex::SharedCfg;

use crate::cache::CacheFile;

#[derive(Parser, Debug)]
#[command(name = "rgeocache-server")]
struct Args {
    /// Path to the v2 cache file (.rgc)
    #[arg(short, long)]
    points: PathBuf,

    /// Listen address
    #[arg(long, default_value = "0.0.0.0:8080")]
    listen: String,

    /// Search radius in degrees
    #[arg(long, default_value_t = 0.01)]
    search_radius: f64,

    /// Number of HTTP worker threads (default: number of CPU cores)
    #[arg(long)]
    workers: Option<usize>,

    /// Maximum request body size in bytes (default: 32 MiB)
    #[arg(long, default_value_t = 64 * 1024 * 1024)]
    max_request_size: usize,
}

#[ntex::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("info")).init();

    #[cfg(feature = "tracing")]
    {
        tracing_subscriber::fmt()
            .with_env_filter(tracing_subscriber::EnvFilter::from_default_env())
            .init();
        log::info!("Tracing enabled");
    }

    let args = Args::parse();

    log::info!("Loading v2 cache from: {}", args.points.display());

    let cache = Arc::new(CacheFile::open(
        args.points.to_str().ok_or("invalid path")?,
    )?);

    let geocoder = geocoder::Geocoder::load(cache.clone(), args.search_radius)?;

    let metrics = server::Metrics::new()?;

    let forward_geocoder_once = Arc::new(OnceLock::new());

    let forward_geocoder_once_clone = forward_geocoder_once.clone();
    thread::spawn(move || {
        // Store the failure rather than panicking: a panicking build thread would
        // leave the OnceLock empty and block every /fgeocode/search request
        // forever on `wait()`.
        let result = forward_geocoder::ForwardGeocoder::build(cache.clone()).map_err(|err| {
            log::error!("failed to build forward geocoder index: {err}");
            err.to_string()
        });
        let _ = forward_geocoder_once_clone.set(result);
    });

    let state = Arc::new(server::AppState {
        geocoder: Arc::new(geocoder),
        forward_geocoder: forward_geocoder_once,
        metrics,
    });

    log::info!("Starting server on {}", args.listen);

    let max_request_size = args.max_request_size;

    let mut srv = HttpServer::new(async move || {
        ntex::web::App::new()
            .state(state.clone())
            .state(ntex::web::types::JsonConfig::default().limit(max_request_size))
            .route(
                "/rgeocode/address/{lat}/{lon}",
                ntex::web::get().to(server::rgeocode_handler),
            )
            .route(
                "/rgeocode/multiaddress",
                ntex::web::post().to(server::rgeocode_multi_handler),
            )
            .route(
                "/fgeocode/search",
                ntex::web::get().to(server::fgeocode_handle),
            )
            .route("/metrics", ntex::web::get().to(server::metrics_handler))
    })
    .config(
        SharedCfg::new("rgeocache")
            .add(IoConfig::default())
            .add(HttpServiceConfig::default())
            .add(WebAppConfig::default()),
    );

    if let Some(w) = args.workers {
        srv = srv.workers(w);
    }

    srv.bind(&args.listen)?.run().await?;

    Ok(())
}
