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

/// Low-memory reverse geocoding server (v2 cache, mmap-only).
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

    let state = Arc::new(server::AppState { geocoder, metrics });

    log::info!("Starting server on {}", args.listen);

    HttpServer::new(async move || {
        ntex::web::App::new()
            .state(state.clone())
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
    )
    // .workers(1)
    .bind(&args.listen)?
    .run()
    .await?;

    Ok(())
}
