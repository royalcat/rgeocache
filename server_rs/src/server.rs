//! ntex HTTP server handlers and metrics.

use crate::forward_geocoder::{
    ForwardGeocoder, GeocodeKindFilter, SearchRequest, StructuredQuery, Suggestion, DEFAULT_LIMIT,
    MAX_LIMIT, MAX_QUERY_LEN,
};
use crate::geocoder::{Geocoder, Info};
use crate::road_graph::{self, BBox, RoadGraph};
use async_stream::try_stream;
use futures::Stream;
use geo::MultiPolygon;
use ntex::http::header;
use ntex::util::Bytes;
use ntex::web::{self, HttpResponse};
use prometheus::{Counter, Encoder, Histogram, HistogramOpts, Opts, Registry, TextEncoder};
use rayon::iter::{IntoParallelIterator, ParallelIterator};
use std::collections::{BTreeSet, HashMap};
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
    /// Parsed road graph section of the loaded cache, when present.
    pub road_graph: Option<Arc<RoadGraph>>,
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
    pub roadgraph_requests: Counter,
    pub roadgraph_duration: Histogram,
    pub roadgraph_edges: Histogram,
    pub roadgraph_truncated: Counter,
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

        let roadgraph_requests = Counter::new(
            "rgeocode_roadgraph_requests_total",
            "Total road graph bbox requests",
        )?;
        registry.register(Box::new(roadgraph_requests.clone()))?;

        let roadgraph_duration = Histogram::with_opts(
            HistogramOpts::new(
                "rgeocode_roadgraph_duration_seconds",
                "Road graph bbox query duration",
            )
            .buckets(vec![0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5]),
        )?;
        registry.register(Box::new(roadgraph_duration.clone()))?;

        let roadgraph_edges = Histogram::with_opts(
            HistogramOpts::new(
                "rgeocode_roadgraph_edges",
                "Number of graph edges returned per bbox request",
            )
            .buckets(vec![
                0.0, 1.0, 10.0, 50.0, 100.0, 500.0, 1000.0, 5000.0, 10000.0, 50000.0,
            ]),
        )?;
        registry.register(Box::new(roadgraph_edges.clone()))?;

        let roadgraph_truncated = Counter::new(
            "rgeocode_roadgraph_truncated_total",
            "Road graph bbox requests truncated at the edge limit",
        )?;
        registry.register(Box::new(roadgraph_truncated.clone()))?;

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
            roadgraph_requests,
            roadgraph_duration,
            roadgraph_edges,
            roadgraph_truncated,
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

// ---------------------------------------------------------------------------
// Road graph
// ---------------------------------------------------------------------------

const ROADGRAPH_DEFAULT_LIMIT: usize = 10_000;
const ROADGRAPH_MAX_LIMIT: usize = 50_000;

#[derive(Debug, serde::Deserialize)]
pub struct RoadGraphQuery {
    /// `min_lon,min_lat,max_lon,max_lat` (GeoJSON axis order).
    bbox: String,
    /// Include per-edge direction metadata; defaults to false.
    directions: Option<bool>,
    /// Merge connected edges into polylines and drop vertices that lie within
    /// [`road_graph::SIMPLIFY_EPSILON`] of the straight chord. Defaults to true;
    /// `simplify=false` returns one raw two-point edge per graph edge.
    simplify: Option<bool>,
    /// Maximum number of raw edges; clamped to `1..=ROADGRAPH_MAX_LIMIT`.
    limit: Option<usize>,
}

/// Parses and validates `min_lon,min_lat,max_lon,max_lat`.
fn parse_bbox(value: &str) -> Result<BBox, String> {
    let parts: Vec<&str> = value.split(',').collect();
    if parts.len() != 4 {
        return Err("bbox must be min_lon,min_lat,max_lon,max_lat".to_string());
    }

    let mut coords = [0.0f64; 4];
    for (i, part) in parts.iter().enumerate() {
        let part = part.trim();
        let value: f64 = part
            .parse()
            .map_err(|_| format!("invalid bbox coordinate '{part}'"))?;
        if !value.is_finite() {
            return Err("bbox coordinates must be finite numbers".to_string());
        }
        coords[i] = value;
    }

    let bbox = BBox {
        min_lon: coords[0],
        min_lat: coords[1],
        max_lon: coords[2],
        max_lat: coords[3],
    };
    if bbox.min_lon < -180.0 || bbox.max_lon > 180.0 || bbox.min_lat < -90.0 || bbox.max_lat > 90.0
    {
        return Err("bbox coordinates out of range".to_string());
    }
    if bbox.min_lon >= bbox.max_lon || bbox.min_lat >= bbox.max_lat {
        return Err("bbox min values must be smaller than max values".to_string());
    }

    Ok(bbox)
}

/// `GET /roadgraph/box` — road graph edges intersecting a bounding box as a
/// GeoJSON FeatureCollection, with endpoint node features.
pub async fn roadgraph_box_handler(
    state: web::types::State<Arc<AppState>>,
    web::types::Query(query_params): web::types::Query<RoadGraphQuery>,
) -> HttpResponse {
    state.metrics.roadgraph_requests.inc();

    let bbox = match parse_bbox(&query_params.bbox) {
        Ok(bbox) => bbox,
        Err(err) => return HttpResponse::BadRequest().body(err),
    };

    let Some(graph) = state.road_graph.clone() else {
        return HttpResponse::ServiceUnavailable().body("cache has no road graph section");
    };

    let directions = query_params.directions.unwrap_or(false);
    let simplify = query_params.simplify.unwrap_or(true);
    let limit = query_params
        .limit
        .unwrap_or(ROADGRAPH_DEFAULT_LIMIT)
        .clamp(1, ROADGRAPH_MAX_LIMIT);

    let _timer = state.metrics.roadgraph_duration.start_timer();
    let graph_for_query = graph.clone();
    let queried =
        ntex::rt::spawn_blocking(move || graph_for_query.edges_in_bbox(&bbox, limit)).await;

    let (hits, truncated) = match queried {
        Ok(result) => result,
        Err(err) => {
            return HttpResponse::InternalServerError()
                .body(format!("road graph task failed: {err}"));
        }
    };

    state.metrics.roadgraph_edges.observe(hits.len() as f64);
    if truncated {
        state.metrics.roadgraph_truncated.inc();
    }

    let mut features: Vec<serde_json::Value> = Vec::with_capacity(hits.len() * 2);
    // Every vertex of a returned edge (or of a simplified chain) is included,
    // deduplicated and ordered at the end.
    let mut node_ids: BTreeSet<u32> = BTreeSet::new();

    if simplify {
        for chain in road_graph::simplify_hits(&hits) {
            let coordinates: Vec<[f64; 2]> =
                chain.coords.iter().map(|&(lon, lat)| [lon, lat]).collect();
            features.push(serde_json::json!({
                "type": "Feature",
                "geometry": {"type": "LineString", "coordinates": coordinates},
                "properties": edge_properties(&graph, &chain.edge, directions),
            }));
            for pos in chain.positions {
                node_ids.insert(pos);
            }
        }
    } else {
        for hit in &hits {
            features.push(serde_json::json!({
                "type": "Feature",
                "geometry": {
                    "type": "LineString",
                    "coordinates": [[hit.from.0, hit.from.1], [hit.to.0, hit.to.1]],
                },
                "properties": edge_properties(&graph, &hit.edge, directions),
            }));
            node_ids.insert(hit.edge.from_pos);
            node_ids.insert(hit.edge.to_pos);
        }
    }

    for id in node_ids {
        if let Some((lon, lat)) = graph.coord(id) {
            features.push(serde_json::json!({
                "type": "Feature",
                "geometry": {"type": "Point", "coordinates": [lon, lat]},
                "properties": {"id": id},
            }));
        }
    }

    let body = serde_json::json!({
        "type": "FeatureCollection",
        "features": features,
        "truncated": truncated,
    });
    HttpResponse::Ok().json(&body)
}

/// GeoJSON properties of one edge (or simplified chain).
fn edge_properties(
    graph: &RoadGraph,
    edge: &road_graph::GraphEdge,
    directions: bool,
) -> serde_json::Map<String, serde_json::Value> {
    let mut properties = serde_json::Map::new();
    properties.insert("from".to_string(), serde_json::json!(edge.from_pos));
    properties.insert("to".to_string(), serde_json::json!(edge.to_pos));

    let name = graph.string(edge.name_id);
    if !name.is_empty() {
        properties.insert("name".to_string(), serde_json::json!(name));
    }
    let street = graph.string(edge.street_id);
    if !street.is_empty() {
        properties.insert("street".to_string(), serde_json::json!(street));
    }
    properties.insert(
        "class".to_string(),
        serde_json::json!(road_graph::class_name(edge.class)),
    );
    if directions {
        properties.insert(
            "direction".to_string(),
            serde_json::json!(road_graph::direction_name(edge.oneway)),
        );
    }
    properties
}

/// The self-contained browser demo page for the forward geocoding API, shared
/// with the Go server.
///
/// This embeds `server_rs/web/fgeocode-demo.html`, a synced copy of the shared
/// `web/fgeocode-demo.html`: the file must live inside this crate because the
/// Docker build context is `server_rs/` (plain `docker build .`) and BuildKit
/// refuses to follow symlinks that leave the context. The
/// `demo_page_copy_matches_shared_page` test keeps the copy identical.
///
/// Compiled in only with the non-default `demo-page` feature, so a default
/// build neither embeds the page nor depends on the copied file.
#[cfg(feature = "demo-page")]
const FGEODEMO_HTML: &str = include_str!("../web/fgeocode-demo.html");

/// `GET /fgeocode/demo`: a static page that drives `/fgeocode/search` from the
/// browser. It is served even while the index is building or after a failed
/// build, so the page itself can surface that 503 reason.
///
/// Requires the non-default `demo-page` Cargo feature.
#[cfg(feature = "demo-page")]
pub async fn fgeocode_demo_handle() -> HttpResponse {
    HttpResponse::Ok()
        .content_type("text/html; charset=utf-8")
        .body(FGEODEMO_HTML)
}

/// The self-contained browser demo page for the road graph API, shared asset
/// mirrored from `web/roadgraph-demo.html` (same Docker build context reason as
/// the forward geocoding page). The page pulls Leaflet and OSM tiles from the
/// network at runtime.
///
/// Compiled in only with the non-default `demo-page` feature.
#[cfg(feature = "demo-page")]
const ROADGRAPH_DEMO_HTML: &str = include_str!("../web/roadgraph-demo.html");

/// `GET /roadgraph/demo`: a static page that drives `/roadgraph/box` from the
/// browser. It is served even when the cache has no road graph section, so the
/// page itself can surface that 503 reason.
///
/// Requires the non-default `demo-page` Cargo feature.
#[cfg(feature = "demo-page")]
pub async fn roadgraph_demo_handle() -> HttpResponse {
    HttpResponse::Ok()
        .content_type("text/html; charset=utf-8")
        .body(ROADGRAPH_DEMO_HTML)
}

// The demo routes are the only thing this module tests, so the whole module is
// gated with them; otherwise its imports would be unused in default builds.
#[cfg(all(test, feature = "demo-page"))]
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

    #[ntex::test]
    async fn roadgraph_demo_serves_html_page() {
        let app = test::init_service(
            App::new().route("/roadgraph/demo", web::get().to(roadgraph_demo_handle)),
        )
        .await;

        let req = test::TestRequest::get().uri("/roadgraph/demo").to_request();
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
        assert!(html.contains("id=\"min-lon\""), "bbox inputs missing");
        assert!(
            html.contains("/roadgraph/box"),
            "road graph endpoint missing"
        );
        assert!(html.contains("leaflet"), "leaflet assets missing");
    }
}

/// Guards `server_rs/web/*.html` against drifting from the shared pages in
/// `web/`. Deliberately not behind `demo-page`, so it runs in the
/// default-feature CI build and fails as soon as the two copies differ.
#[cfg(test)]
mod demo_asset_sync {
    #[test]
    fn fgeocode_demo_page_copy_matches_shared_page() {
        assert_eq!(
            include_str!("../web/fgeocode-demo.html"),
            include_str!("../../web/fgeocode-demo.html"),
            "server_rs/web/fgeocode-demo.html is out of sync with web/fgeocode-demo.html; copy the shared page over"
        );
    }

    #[test]
    fn roadgraph_demo_page_copy_matches_shared_page() {
        assert_eq!(
            include_str!("../web/roadgraph-demo.html"),
            include_str!("../../web/roadgraph-demo.html"),
            "server_rs/web/roadgraph-demo.html is out of sync with web/roadgraph-demo.html; copy the shared page over"
        );
    }
}

#[cfg(test)]
mod roadgraph_query_tests {
    use super::*;

    #[test]
    fn parses_valid_bbox() {
        let bbox = parse_bbox("-0.13, 51.50, -0.12, 51.51").expect("valid bbox");
        assert_eq!(bbox.min_lon, -0.13);
        assert_eq!(bbox.min_lat, 51.50);
        assert_eq!(bbox.max_lon, -0.12);
        assert_eq!(bbox.max_lat, 51.51);
    }

    #[test]
    fn rejects_invalid_bboxes() {
        for value in [
            "",
            "1,2,3",
            "1,2,3,4,5",
            "a,2,3,4",
            "nan,0,1,1",
            "inf,0,1,1",
            "-200,0,1,1",
            "0,0,181,1",
            "0,0,0,1",
            "0,1,1,1",
        ] {
            assert!(
                parse_bbox(value).is_err(),
                "expected {value:?} to be rejected"
            );
        }
    }

    #[test]
    fn roadgraph_query_parses_simplify() {
        let query: RoadGraphQuery = serde_json::from_str(r#"{"bbox":"0,0,1,1"}"#).expect("query");
        assert_eq!(query.simplify, None);
        assert!(query.simplify.unwrap_or(true), "simplify defaults to true");

        let query: RoadGraphQuery =
            serde_json::from_str(r#"{"bbox":"0,0,1,1","simplify":false}"#).expect("query");
        assert_eq!(query.simplify, Some(false));
    }
}
