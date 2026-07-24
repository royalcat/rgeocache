//! ntex HTTP server handlers and metrics.

use async_stream::try_stream;
use futures::Stream;
use ntex::http::header;
use ntex::util::Bytes;
use ntex::web::{self, HttpResponse};
use prometheus::{Counter, Encoder, Histogram, HistogramOpts, Opts, Registry, TextEncoder};
use rayon::iter::{IntoParallelIterator, ParallelIterator};
use std::collections::HashMap;
use std::sync::Arc;

use crate::geocoder::{Geocoder, Info};

// ---------------------------------------------------------------------------
// Application state
// ---------------------------------------------------------------------------

pub struct AppState {
    pub geocoder: Arc<Geocoder>,
    pub metrics: Metrics,
}

// ---------------------------------------------------------------------------
// Metrics (always on)
// ---------------------------------------------------------------------------

pub struct Metrics {
    pub requests_single: Counter,
    pub requests_multi: Counter,
    pub addresses_total: Counter,
    pub lookup_duration: Histogram,
    registry: Registry,
}

impl Metrics {
    pub fn new() -> Result<Self, Box<dyn std::error::Error>> {
        let registry = Registry::new();

        let mut single_opts =
            Opts::new("rgeocode_requests_total", "Total reverse geocode requests");
        single_opts.const_labels = HashMap::from([("endpoint".into(), "address".into())]);
        let requests_single = Counter::with_opts(single_opts)?;
        registry.register(Box::new(requests_single.clone()))?;

        let mut multi_opts = Opts::new("rgeocode_requests_total", "Total reverse geocode requests");
        multi_opts.const_labels = HashMap::from([("endpoint".into(), "multiaddress".into())]);
        let requests_multi = Counter::with_opts(multi_opts)?;
        registry.register(Box::new(requests_multi.clone()))?;

        let addresses_total = Counter::new(
            "rgeocode_addresses_total",
            "Total individual addresses resolved",
        )?;
        registry.register(Box::new(addresses_total.clone()))?;

        let lookup_duration = Histogram::with_opts(
            HistogramOpts::new(
                "rgeocode_lookup_duration_seconds",
                "Reverse geocode lookup duration",
            )
            .buckets(vec![0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1]),
        )?;
        registry.register(Box::new(lookup_duration.clone()))?;

        Ok(Self {
            requests_single,
            requests_multi,
            addresses_total,
            lookup_duration,
            registry,
        })
    }

    pub fn encode(&self) -> Result<String, Box<dyn std::error::Error>> {
        let encoder = TextEncoder::new();
        let metric_families = self.registry.gather();
        let mut buffer = Vec::new();
        encoder.encode(&metric_families, &mut buffer)?;
        Ok(String::from_utf8(buffer)?)
    }
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

/// GET /rgeocode/address/{lat}/{lon}
pub async fn rgeocode_handler(
    state: web::types::State<Arc<AppState>>,
    path: web::types::Path<(String, String)>,
) -> HttpResponse {
    state.metrics.requests_single.inc();
    let _timer = state.metrics.lookup_duration.start_timer();

    let (lat_s, lon_s) = path.into_inner();
    let lat: f64 = match lat_s.parse() {
        Ok(v) => v,
        Err(_) => return HttpResponse::BadRequest().body("invalid latitude"),
    };
    let lon: f64 = match lon_s.parse() {
        Ok(v) => v,
        Err(_) => return HttpResponse::BadRequest().body("invalid longitude"),
    };

    match state.geocoder.find(lat, lon) {
        Some(info) => HttpResponse::Ok().json(&info),
        None => HttpResponse::NoContent().finish(),
    }
}

/// POST /rgeocode/multiaddress — JSON array of [lat, lon] pairs.
pub async fn rgeocode_multi_handler(
    state: web::types::State<Arc<AppState>>,
    body: web::types::Json<Vec<[f64; 2]>>,
) -> HttpResponse {
    state.metrics.requests_multi.inc();
    let points = body.into_inner();
    state.metrics.addresses_total.inc_by(points.len() as f64);

    let geocoder = state.geocoder.clone();

    let results = async_rayon::spawn_fifo(move || {
        points
            .into_par_iter()
            .map(|[lat, lon]| {
                geocoder.find(lat, lon).unwrap_or_else(|| Info {
                    name: String::new(),
                    street: String::new(),
                    house_number: String::new(),
                    city: String::new(),
                    region: String::new(),
                    country: String::new(),
                    weight: 0,
                })
            })
            .collect::<Vec<_>>()
    })
    .await;

    HttpResponse::Ok()
        .content_type("application/json")
        .streaming(Box::pin(serialize_results(results)))
}

fn serialize_results(
    results: impl IntoIterator<Item = Info>,
) -> impl Stream<Item = Result<ntex::util::Bytes, serde_json::Error>> {
    try_stream! {
        yield Bytes::from_static(b"[");
        let mut first = true;

        for info in results {
            if !first {
                yield Bytes::from_static(b",");
            }
            first = false;

            let json = serde_json::to_string(&info)?;
            yield Bytes::from(json);

        }
        yield Bytes::from_static(b"]");
    }
}

/// GET /metrics — Prometheus text format.
pub async fn metrics_handler(state: web::types::State<Arc<AppState>>) -> HttpResponse {
    match state.metrics.encode() {
        Ok(text) => HttpResponse::Ok()
            .header(header::CONTENT_TYPE, "text/plain; version=0.0.4")
            .body(text),
        Err(_) => HttpResponse::InternalServerError().finish(),
    }
}
