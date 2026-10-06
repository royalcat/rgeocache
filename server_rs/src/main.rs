use std::path::PathBuf;
use std::sync::{Arc, OnceLock};
use std::thread;

use clap::Parser;
use ntex::http::HttpServiceConfig;
use ntex::io::IoConfig;
use ntex::web::{HttpServer, WebAppConfig};
use ntex::SharedCfg;

use rgeocache_server::cache::CacheFile;
use rgeocache_server::{forward_geocoder, geocoder, server};

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

    /// Directory for the forward geocoder index
    ///
    /// The index is reused on restart when it was built from the same cache
    /// (a fingerprint is stored alongside it) and rebuilt otherwise. Without
    /// this flag the index is built in a temporary directory and deleted on
    /// exit.
    #[arg(long)]
    index_dir: Option<PathBuf>,
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

    let geocoder = Arc::new(geocoder::Geocoder::load(cache.clone(), args.search_radius)?);

    let metrics = server::Metrics::new()?;

    if let Some(dir) = &args.index_dir {
        forward_geocoder::validate_index_dir(dir)
            .map_err(|err| format!("invalid --index-dir {}: {err}", dir.display()))?;
    }

    let forward_geocoder_once = Arc::new(OnceLock::new());

    let index_dir = args.index_dir.clone();
    let forward_geocoder_once_clone = forward_geocoder_once.clone();
    // The forward geocoder resolves each point's country through the same border
    // trees the reverse geocoder uses, so it borrows the already-built geocoder
    // instead of loading them twice.
    let geocoder_for_index = geocoder.clone();
    let metrics_for_index = metrics.clone();
    thread::spawn(move || {
        let started = std::time::Instant::now();
        // Store the failure rather than panicking: a panicking build thread would
        // leave the OnceLock empty, and every /fgeocode/search request would keep
        // answering 503 as if the build were still running, with no error shown.
        let result =
            forward_geocoder::ForwardGeocoder::build(geocoder_for_index, index_dir.as_deref())
                .map(|(geocoder, mode)| {
                    metrics_for_index
                        .fgeocode_index_build_duration
                        .observe(started.elapsed().as_secs_f64());
                    match mode {
                        forward_geocoder::IndexMode::Built => {
                            metrics_for_index.fgeocode_index_built.inc()
                        }
                        forward_geocoder::IndexMode::Reused => {
                            metrics_for_index.fgeocode_index_reused.inc()
                        }
                    }
                    Arc::new(geocoder)
                })
                .map_err(|err| {
                    metrics_for_index
                        .fgeocode_index_build_duration
                        .observe(started.elapsed().as_secs_f64());
                    metrics_for_index.fgeocode_index_failures.inc();
                    log::error!("failed to build forward geocoder index: {err}");
                    err.to_string()
                });
        let _ = forward_geocoder_once_clone.set(result);
    });

    let state = Arc::new(server::AppState {
        geocoder,
        forward_geocoder: forward_geocoder_once,
        metrics,
    });

    log::info!("Starting server on {}", args.listen);

    let max_request_size = args.max_request_size;

    let mut srv = HttpServer::new(async move || {
        let app = ntex::web::App::new()
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
            .route(
                "/fgeocode/autocomplete",
                ntex::web::get().to(server::fgeocode_autocomplete_handle),
            );
        // The demo page is a non-default feature: without it the route does not
        // exist and the handler is not compiled in.
        #[cfg(feature = "demo-page")]
        let app = app.route(
            "/fgeocode/demo",
            ntex::web::get().to(server::fgeocode_demo_handle),
        );
        app.route("/metrics", ntex::web::get().to(server::metrics_handler))
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
