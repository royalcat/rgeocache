mod border_tree;
mod cache;
mod geocoder;
mod proto;
mod server;

use std::path::PathBuf;
use std::sync::Arc;

use clap::Parser;
use ntex::http::HttpServiceConfig;
use ntex::io::IoConfig;
use ntex::web::{HttpServer, WebAppConfig};
use ntex::SharedCfg;

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

    let geocoder = geocoder::Geocoder::load(
        args.points.to_str().ok_or("invalid path")?,
        args.search_radius,
    )?;

    let metrics = server::Metrics::new()?;

    let state = Arc::new(server::AppState {
        geocoder: Arc::new(geocoder),
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
