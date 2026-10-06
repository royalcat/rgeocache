//! ntex HTTP server handlers and metrics.

use crate::forward_geocoder::{
    ForwardGeocoder, GeocodeKindFilter, SearchRequest, StructuredQuery, Suggestion, DEFAULT_LIMIT,
    MAX_LIMIT, MAX_QUERY_LEN,
};
use crate::geocoder::{Geocoder, Info};
use async_stream::try_stream;
use futures::Stream;
use geo::MultiPolygon;
use ntex::http::header;
use ntex::util::Bytes;
use ntex::web::{self, HttpResponse};
use prometheus::{Counter, Encoder, Histogram, HistogramOpts, Opts, Registry, TextEncoder};
use rayon::iter::{IntoParallelIterator, ParallelIterator};
use std::collections::HashMap;
use std::sync::{Arc, OnceLock};

// ---------------------------------------------------------------------------
// Application state
// ---------------------------------------------------------------------------

pub struct AppState {
    pub geocoder: Arc<Geocoder>,
    /// `Err` carries the build failure so the handler can answer 503 instead of
    /// blocking forever on a `OnceLock` that will never be filled. While the
    /// lock is still empty the handler answers 503 with `Retry-After` rather
    /// than blocking an ntex worker for the duration of the build.
    pub forward_geocoder: Arc<OnceLock<Result<Arc<ForwardGeocoder>, String>>>,
    pub metrics: Metrics,
}

// ---------------------------------------------------------------------------
// Metrics (always on)
// ---------------------------------------------------------------------------

#[derive(Clone)]
pub struct Metrics {
    pub requests_single: Counter,
    pub requests_multi: Counter,
    pub addresses_total: Counter,
    pub lookup_duration: Histogram,
    pub fgeocode_requests: Counter,
    pub fgeocode_suggest_requests: Counter,
    pub fgeocode_duration: Histogram,
    pub fgeocode_results: Histogram,
    pub fgeocode_zero_results: Counter,
    pub fgeocode_index_build_duration: Histogram,
    pub fgeocode_index_built: Counter,
    pub fgeocode_index_reused: Counter,
    pub fgeocode_index_failures: Counter,
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

        let fgeocode_requests = Counter::new(
            "rgeocode_fgeocode_requests_total",
            "Total forward geocode requests",
        )?;
        registry.register(Box::new(fgeocode_requests.clone()))?;

        let fgeocode_suggest_requests = Counter::new(
            "rgeocode_fgeocode_suggest_requests_total",
            "Total forward geocode autocomplete requests",
        )?;
        registry.register(Box::new(fgeocode_suggest_requests.clone()))?;

        let fgeocode_duration = Histogram::with_opts(
            HistogramOpts::new(
                "rgeocode_fgeocode_search_duration_seconds",
                "Forward geocode search duration",
            )
            .buckets(vec![0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5]),
        )?;
        registry.register(Box::new(fgeocode_duration.clone()))?;

        let fgeocode_results = Histogram::with_opts(
            HistogramOpts::new(
                "rgeocode_fgeocode_results",
                "Number of results returned per forward geocode request",
            )
            .buckets(vec![0.0, 1.0, 2.0, 3.0, 5.0, 10.0, 20.0, 50.0, 100.0]),
        )?;
        registry.register(Box::new(fgeocode_results.clone()))?;

        let fgeocode_zero_results = Counter::new(
            "rgeocode_fgeocode_zero_results_total",
            "Forward geocode requests that returned no results",
        )?;
        registry.register(Box::new(fgeocode_zero_results.clone()))?;

        let fgeocode_index_build_duration = Histogram::with_opts(
            HistogramOpts::new(
                "rgeocode_fgeocode_index_build_duration_seconds",
                "Time spent building the forward geocoder index",
            )
            .buckets(vec![0.0, 1.0, 5.0, 10.0, 30.0, 60.0, 120.0, 300.0]),
        )?;
        registry.register(Box::new(fgeocode_index_build_duration.clone()))?;

        let fgeocode_index_built = Counter::new(
            "rgeocode_fgeocode_index_built_total",
            "Forward geocoder index builds",
        )?;
        registry.register(Box::new(fgeocode_index_built.clone()))?;

        let fgeocode_index_reused = Counter::new(
            "rgeocode_fgeocode_index_reused_total",
            "Forward geocoder index reuses",
        )?;
        registry.register(Box::new(fgeocode_index_reused.clone()))?;

        let fgeocode_index_failures = Counter::new(
            "rgeocode_fgeocode_index_failures_total",
            "Forward geocoder index build failures",
        )?;
        registry.register(Box::new(fgeocode_index_failures.clone()))?;

        Ok(Self {
            requests_single,
            requests_multi,
            addresses_total,
            lookup_duration,
            fgeocode_requests,
            fgeocode_suggest_requests,
            fgeocode_duration,
            fgeocode_results,
            fgeocode_zero_results,
            fgeocode_index_build_duration,
            fgeocode_index_built,
            fgeocode_index_reused,
            fgeocode_index_failures,
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

#[derive(Debug, serde::Deserialize)]
pub struct GeocodeQueryRequest {
    /// Free-text query. Optional when at least one structured field is given.
    q: Option<String>,
    /// Comma-separated object kinds to include: `zone`, `building`, `road`,
    /// `area` (aliases `z`/`b`/`r`/`a`). Absent means all kinds.
    kind: Option<String>,
    /// Maximum number of results, clamped to `1..=MAX_LIMIT`.
    limit: Option<usize>,
    /// Number of results to skip. Pagination is shallow (bounded by the
    /// collector's over-fetch), so a large offset yields fewer results.
    offset: Option<usize>,
    /// Include the full multipolygon for zone hits; defaults to true.
    include_polygon: Option<bool>,
    /// Structured address fields, each matched only against its own field.
    city: Option<String>,
    region: Option<String>,
    street: Option<String>,
    house: Option<String>,
    name: Option<String>,
}

#[derive(Debug, serde::Serialize)]
pub struct GeocodeResponse {
    results: Vec<GeocodeResponseItem>,
}

#[derive(Debug, serde::Serialize)]
pub struct GeocodeResponseItem {
    address_string: String,
    score: f32,
    point: [f64; 2],
    geo_type: String,
    country: String,
    #[serde(skip_serializing_if = "Option::is_none", default)]
    multipolygon: Option<MultiPolygon>,
}

pub async fn fgeocode_handle(
    state: web::types::State<Arc<AppState>>,
    web::types::Query(query_params): web::types::Query<GeocodeQueryRequest>,
) -> impl web::Responder {
    state.metrics.fgeocode_requests.inc();

    if query_params.q.as_deref().map_or(0, str::len) > MAX_QUERY_LEN {
        return HttpResponse::BadRequest().body("query too long");
    }

    let structured = StructuredQuery {
        city: query_params.city,
        region: query_params.region,
        street: query_params.street,
        house: query_params.house,
        name: query_params.name,
    };
    for value in [
        &structured.city,
        &structured.region,
        &structured.street,
        &structured.house,
        &structured.name,
    ]
    .into_iter()
    .flatten()
    {
        if value.len() > MAX_QUERY_LEN {
            return HttpResponse::BadRequest().body("structured field too long");
        }
    }
    let request = SearchRequest {
        query: query_params.q,
        structured,
        kind: query_params
            .kind
            .as_deref()
            .map(GeocodeKindFilter::from)
            .unwrap_or_default(),
        limit: query_params
            .limit
            .unwrap_or(DEFAULT_LIMIT)
            .clamp(1, MAX_LIMIT),
        offset: query_params.offset.unwrap_or(0),
        include_polygon: query_params.include_polygon.unwrap_or(true),
    };
    if request.query.as_deref().is_none_or(|q| q.trim().is_empty())
        && !request.structured.has_values()
    {
        return HttpResponse::BadRequest().body("q or a structured field is required");
    }

    // Do not block an ntex worker while the index builds: answer 503 and let
    // the client retry. A blocking `wait()` here would let a burst of forward
    // requests occupy every worker and stall reverse geocoding too.
    let forward_geocoder = match forward_geocoder(&state) {
        Ok(geocoder) => geocoder,
        Err(response) => return response,
    };

    let _timer = state.metrics.fgeocode_duration.start_timer();
    let searched = ntex::rt::spawn_blocking(move || forward_geocoder.search(&request)).await;

    let results = match searched {
        Ok(Ok(result)) => result,
        Ok(Err(err)) => return HttpResponse::InternalServerError().body(err.to_string()),
        Err(err) => {
            return HttpResponse::InternalServerError().body(format!("search task failed: {err}"));
        }
    };

    state.metrics.fgeocode_results.observe(results.len() as f64);
    if results.is_empty() {
        state.metrics.fgeocode_zero_results.inc();
    }

    let resp = GeocodeResponse {
        results: results
            .into_iter()
            .map(|v| GeocodeResponseItem {
                address_string: v.address_string,
                score: v.score,
                point: [v.point.0, v.point.1],
                geo_type: v.geo_type.to_string(),
                country: v.country,
                multipolygon: v.multipolygon,
            })
            .collect(),
    };

    web::HttpResponse::Ok().json(&resp)
}

/// Fetch the ready forward geocoder or build the 503 response explaining why it
/// is not available yet.
#[allow(clippy::result_large_err)]
fn forward_geocoder(state: &AppState) -> Result<Arc<ForwardGeocoder>, HttpResponse> {
    match state.forward_geocoder.get() {
        Some(Ok(geocoder)) => Ok(geocoder.clone()),
        Some(Err(err)) => {
            Err(HttpResponse::ServiceUnavailable()
                .body(format!("forward geocoder unavailable: {err}")))
        }
        None => Err(HttpResponse::ServiceUnavailable()
            .header(header::RETRY_AFTER, "5")
            .body("forward geocoder index is still building")),
    }
}

#[derive(Debug, serde::Deserialize)]
pub struct AutocompleteQueryRequest {
    q: String,
    limit: Option<usize>,
}

#[derive(Debug, serde::Serialize)]
pub struct AutocompleteResponse {
    suggestions: Vec<Suggestion>,
}

pub async fn fgeocode_autocomplete_handle(
    state: web::types::State<Arc<AppState>>,
    web::types::Query(query_params): web::types::Query<AutocompleteQueryRequest>,
) -> impl web::Responder {
    state.metrics.fgeocode_suggest_requests.inc();

    if query_params.q.len() > MAX_QUERY_LEN {
        return HttpResponse::BadRequest().body("query too long");
    }

    let forward_geocoder = match forward_geocoder(&state) {
        Ok(geocoder) => geocoder,
        Err(response) => return response,
    };

    let limit = query_params
        .limit
        .unwrap_or(DEFAULT_LIMIT)
        .clamp(1, MAX_LIMIT);
    let query = query_params.q;
    let suggested = ntex::rt::spawn_blocking(move || forward_geocoder.suggest(&query, limit)).await;

    match suggested {
        Ok(Ok(suggestions)) => web::HttpResponse::Ok().json(&AutocompleteResponse { suggestions }),
        Ok(Err(err)) => HttpResponse::InternalServerError().body(err.to_string()),
        Err(err) => HttpResponse::InternalServerError().body(format!("suggest task failed: {err}")),
    }
}

/// The self-contained browser demo page for the forward geocoding API, shared
/// with the Go server (`web/fgeocode-demo.html`).
const FGEODEMO_HTML: &str = include_str!("../../web/fgeocode-demo.html");

/// `GET /fgeocode/demo`: a static page that drives `/fgeocode/search` from the
/// browser. It is served even while the index is building or after a failed
/// build, so the page itself can surface that 503 reason.
pub async fn fgeocode_demo_handle() -> HttpResponse {
    HttpResponse::Ok()
        .content_type("text/html; charset=utf-8")
        .body(FGEODEMO_HTML)
}

#[cfg(test)]
mod tests {
    use super::*;
    use ntex::http::StatusCode;
    use ntex::web::{test, App};

    #[ntex::test]
    async fn fgeocode_demo_serves_html_page() {
        let app = test::init_service(
            App::new().route("/fgeocode/demo", web::get().to(fgeocode_demo_handle)),
        )
        .await;

        let req = test::TestRequest::get().uri("/fgeocode/demo").to_request();
        let resp = test::call_service(&app, req).await;

        assert_eq!(resp.status(), StatusCode::OK);
        assert_eq!(
            resp.headers()
                .get(header::CONTENT_TYPE)
                .and_then(|value| value.to_str().ok()),
            Some("text/html; charset=utf-8")
        );

        let body = test::read_body(resp).await;
        let html = std::str::from_utf8(&body).expect("demo page is valid UTF-8");
        assert!(html.contains("id=\"q\""), "search input missing");
        assert!(html.contains("/fgeocode/search"), "search endpoint missing");
    }
}
