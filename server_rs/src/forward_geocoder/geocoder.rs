use std::collections::HashMap;
use std::path::Path;
use std::str::FromStr;
use std::sync::Arc;

use geo::Centroid;
use rayon::prelude::*;
use serde::{Deserialize, Serialize};
use strum::EnumString;
use tantivy::collector::{FilterCollector, TopDocs};
use tantivy::query::{
    BooleanQuery, BoostQuery, ConstScoreQuery, FuzzyTermQuery, Occur, PhrasePrefixQuery,
    PhraseQuery, Query, TermQuery,
};
use tantivy::schema::*;
use tantivy::space_usage::PerFieldSpaceUsage;
use tantivy::{
    doc, DocAddress, Index, IndexReader, IndexWriter, Score, Searcher, TantivyDocument,
    TantivyError, Term,
};
use tantivy::{tokenizer::*, ByteCount};

use super::text_analyzer::{build_analyzers, Analyzers};
use crate::cache::CacheFile;
use crate::geocoder::Geocoder;

// ---------------------------------------------------------------------------
// Tunables
// ---------------------------------------------------------------------------

/// Analyzer for the free-text address parts (region, city, street, name).
const TEXT_ANALYZER: &str = "geo_text";
/// Analyzer for house numbers: no stemming, no accent folding — `12а` must stay `12а`.
const HOUSE_ANALYZER: &str = "geo_house";

/// `FilterCollector` addresses the fast field by name, and `Field` carries no
/// back-reference to the schema, so the name is defined once here.
const GEO_TYPE_FIELD: &str = "geo_type";

// Field boosts. These only *tune* the ranking — most of the discrimination comes
// from BM25's idf, so they are deliberately close together rather than the flat
// 20.0 that was applied to every field previously.
const BOOST_HOUSE: Score = 8.0;
const BOOST_STREET: Score = 4.0;
const BOOST_NAME: Score = 3.0;
const BOOST_CITY: Score = 2.0;
const BOOST_REGION: Score = 1.0;
/// The country field holds one of a handful of values shared by millions of
/// points, so its idf is already tiny; a minimal boost is enough for
/// `country, city, …` queries to resolve the country token.
const BOOST_COUNTRY: Score = 1.0;

/// Multiplier on a clause that matches the query token exactly (or as a prefix,
/// for the token being typed).
///
/// Every token is offered both an exact and a fuzzy clause, so the fuzzy one can
/// fill gaps but must never displace an exact hit. BM25 scores the two off
/// different idfs, and idf varies with document frequency, so the gap has to be
/// wide enough to dominate that spread. This is a strong preference, not a hard
/// guarantee: an exact hit on a term present in nearly every document has an idf
/// near zero and could in principle still lose to a very rare near-miss.
const EXACT_BOOST: Score = 20.0;
/// Multiplier on the clause tolerating one edit.
const FUZZY_BOOST: Score = 1.0;

/// Score added to a document whose `merged` field contains the whole query as
/// an in-order phrase with at most [`PHRASE_SLOP`] intervening tokens.
///
/// A constant rather than a multiplier: per-token BM25 sums are unbounded
/// (fields × idf × term frequency × token count), so no multiplier can
/// *guarantee* that a phrase hit outranks a document that merely matched every
/// token somewhere. A constant makes "the phrase always wins" exact. Within the
/// phrase tier the ordinary base score still orders results, because the total
/// is `base + PHRASE_SCORE`.
const PHRASE_SCORE: Score = 1_000_000.0;

/// Score added to a strictly contiguous phrase — one tier above
/// [`PHRASE_SCORE`], so a literal match outranks a sloppy one however their base
/// scores fall.
const EXACT_PHRASE_SCORE: Score = 2_000_000.0;

/// How many intervening tokens the sloppy phrase tier tolerates.
///
/// One is enough to bridge address type words that the query omits but the
/// stored street carries ("Тверская" vs. "Тверская улица").
const PHRASE_SLOP: u32 = 1;

/// Shortest token that gets prefix (autocomplete) matching. Below this a
/// Levenshtein prefix automaton would enumerate a huge share of the term
/// dictionary, so short tokens are matched exactly.
const MIN_PREFIX_LEN: usize = 3;

/// Comma/semicolon separated address parts, all of which must match.
const MAX_SEGMENTS: usize = 8;

/// Tokens shorter than this are dropped from the query before matching, unless
/// they carry a digit.
///
/// This is what makes abbreviated addresses work: the type words people type
/// ("г", "ул", "д", "кв") are short and appear in no document, and under the AND
/// semantics below a single token that matches nothing makes the whole query
/// unsatisfiable. Dropping them needs no vocabulary, unlike a stopword list.
///
/// The digit exemption is load-bearing — house numbers are short, and a plain
/// length cut would discard `12` and `5` along with the type words.
const MIN_TOKEN_LEN: usize = 3;

// TODO: the length rule above only covers *short* type words. A longer one that
// is absent from the index ("дом", "корпус", "квартира", or "город" in
// "город Москва") still makes the whole AND query unsatisfiable.
//
// The general fix is to drop any token that cannot constrain the query — one
// whose term does not exist in the index — which needs no vocabulary and works
// for every language. It requires the term dictionary at query-build time
// (`IndexReader` / `Searcher::doc_freq`), which today is only available after
// `build_query` has already run.

/// Longest accepted query string. Longer is rejected by the HTTP handler.
pub const MAX_QUERY_LEN: usize = 256;

pub const DEFAULT_LIMIT: usize = 10;
pub const MAX_LIMIT: usize = 100;

// The index holds one document per point, so a common street name matches many
// near-identical rows. Over-fetch, collapse, then truncate.
const OVERFETCH: usize = 10;
const MIN_FETCH: usize = 50;
const MAX_FETCH: usize = 500;

/// Tie-break for equal BM25 scores.
///
/// A single-term query scores every document matching the same term in the same
/// field identically (`score = boost × idf`), so ties are common and the order
/// within them would otherwise be arbitrary. A specific address is a more
/// useful hit than the road that contains it, which in turn beats the
/// region/country polygon. Also makes the response deterministic.
fn kind_rank(obj_type: GeoObjectKind) -> u8 {
    match obj_type {
        GeoObjectKind::Building => 2,
        GeoObjectKind::Road => 1,
        GeoObjectKind::Zone => 0,
    }
}

fn tokenize(analyzer: &mut TextAnalyzer, text: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut stream = analyzer.token_stream(text);
    while stream.advance() {
        out.push(stream.token().text.clone());
    }
    out
}

// ---------------------------------------------------------------------------
// Object kind
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, EnumString, strum::Display)]
#[strum(ascii_case_insensitive)]
pub enum GeoObjectKind {
    #[strum(serialize = "zone", serialize = "z")]
    Zone = 1,
    #[strum(serialize = "building", serialize = "b")]
    Building = 2,
    #[strum(serialize = "road", serialize = "r")]
    Road = 3,
}

impl GeoObjectKind {
    /// Derive the object kind from the cache `weight` byte.
    ///
    /// TODO: replace with an explicit object-type field once the cache format
    /// carries one. `weight` is a lossy proxy — it also encodes the area
    /// sub-kind (3 = industrial, 2 = protected), and those surface as
    /// `Building` for now.
    pub fn from_weight(weight: u8) -> GeoObjectKind {
        match weight {
            5 => GeoObjectKind::Road,
            _ => GeoObjectKind::Building,
        }
    }
}

impl TryFrom<u64> for GeoObjectKind {
    type Error = TantivyError;

    fn try_from(value: u64) -> Result<Self, Self::Error> {
        match value {
            1 => Ok(GeoObjectKind::Zone),
            2 => Ok(GeoObjectKind::Building),
            3 => Ok(GeoObjectKind::Road),
            other => Err(TantivyError::InvalidArgument(format!(
                "unknown geo_type value: {other}"
            ))),
        }
    }
}

/// Which object kinds a query should return. `Default` enables all of them.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct GeocodeKindFilter {
    zones: bool,
    buildings: bool,
    roads: bool,
}

impl Default for GeocodeKindFilter {
    fn default() -> Self {
        GeocodeKindFilter {
            zones: true,
            buildings: true,
            roads: true,
        }
    }
}

impl From<&str> for GeocodeKindFilter {
    fn from(s: &str) -> Self {
        let mut filter = GeocodeKindFilter {
            zones: false,
            buildings: false,
            roads: false,
        };
        for part in s.split(',') {
            let part = part.trim();
            if part.is_empty() {
                continue;
            }
            match GeoObjectKind::from_str(part) {
                Ok(GeoObjectKind::Zone) => filter.zones = true,
                Ok(GeoObjectKind::Building) => filter.buildings = true,
                Ok(GeoObjectKind::Road) => filter.roads = true,
                Err(_) => log::warn!("ignoring unknown kind filter value: {part:?}"),
            }
        }
        filter
    }
}

impl GeocodeKindFilter {
    fn matches(&self, obj_type: GeoObjectKind) -> bool {
        match obj_type {
            GeoObjectKind::Zone => self.zones,
            GeoObjectKind::Building => self.buildings,
            GeoObjectKind::Road => self.roads,
        }
    }
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy)]
struct Fields {
    /// Resolved from the country border tree at build time. Index-only: it is
    /// not part of the rendered address, only of matching.
    country: Field,
    region: Field,
    city: Field,
    street: Field,
    house_number: Field,
    name: Field,
    /// Every address part joined in the order the phrase query expects. Not
    /// stored — it exists only to be matched.
    merged: Field,
    geo_type: Field,
    /// Where the document's geometry lives in the cache — see [`IndexedDoc`].
    /// `geo_type` says how to read it.
    cache_location: Field,
}

fn build_schema() -> (Schema, Fields) {
    let mut schema_builder = Schema::builder();

    let text_options = |analyzer: &str| {
        TextOptions::default()
            .set_indexing_options(
                TextFieldIndexing::default()
                    .set_tokenizer(analyzer)
                    .set_index_option(IndexRecordOption::WithFreqs),
            )
            .set_stored()
    };

    // The merged address is only ever matched as a phrase, which needs
    // positions; it is never rendered back into a response, so it is not
    // stored.
    let merged_options = TextOptions::default().set_indexing_options(
        TextFieldIndexing::default()
            .set_tokenizer(TEXT_ANALYZER)
            .set_index_option(IndexRecordOption::WithFreqsAndPositions),
    );

    let fields = Fields {
        country: schema_builder.add_text_field("country", text_options(TEXT_ANALYZER)),
        region: schema_builder.add_text_field("region", text_options(TEXT_ANALYZER)),
        city: schema_builder.add_text_field("city", text_options(TEXT_ANALYZER)),
        street: schema_builder.add_text_field("street", text_options(TEXT_ANALYZER)),
        house_number: schema_builder.add_text_field("house_number", text_options(HOUSE_ANALYZER)),
        name: schema_builder.add_text_field("name", text_options(TEXT_ANALYZER)),
        merged: schema_builder.add_text_field("merged", merged_options),
        geo_type: schema_builder.add_u64_field(GEO_TYPE_FIELD, FAST | STORED),
        cache_location: schema_builder.add_u64_field("cache_location", STORED),
    };

    (schema_builder.build(), fields)
}

// ---------------------------------------------------------------------------
// Index construction
// ---------------------------------------------------------------------------

/// One document's worth of indexed data, decoupled from [`CacheFile`] so the
/// indexer can be exercised against synthetic fixtures in tests.
struct IndexedDoc {
    country: String,
    region: String,
    city: String,
    street: String,
    house_number: String,
    name: String,
    geo_kind: GeoObjectKind,
    /// Where this document's geometry lives in the cache; `geo_type` says how
    /// to read it:
    ///
    /// - point documents: the sorted KD-tree position, resolved through
    ///   `CacheFile::read_coord`;
    /// - zone documents: an index into `CacheFile::zones`.
    ///
    /// Coordinates are deliberately *not* stored: they are high-entropy and
    /// compress to nothing in the doc store, while the cache already holds them.
    cache_location: u64,
}

impl IndexedDoc {
    /// The string indexed into the `merged` field: country, city, street,
    /// house number, name — empty parts skipped.
    ///
    /// The order is a contract: the phrase query matches against exactly this
    /// sequence, so changing it changes which queries get the phrase boost.
    fn merged_address(&self) -> String {
        [
            self.country.as_str(),
            self.city.as_str(),
            self.street.as_str(),
            self.house_number.as_str(),
            self.name.as_str(),
        ]
        .into_iter()
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .collect::<Vec<_>>()
        .join(", ")
    }
}

/// Name of the tantivy meta file, the marker of an index directory.
const META_FILE: &str = "meta.json";

/// Sidecar file recording which cache a persisted index was built from.
const SIDECAR_FILE: &str = "rgeocache-fgeocode.json";

/// Bump to invalidate every persisted index.
const SIDECAR_FORMAT: u32 = 1;

/// Identity of the cache an index was built from.
///
/// A document stores a `cache_location` — a KD-tree position or a zone index —
/// that is only meaningful for the exact cache it was built from, so an index
/// reused with a different cache would silently return wrong coordinates. The
/// sidecar records this fingerprint next to the index; any mismatch forces a
/// rebuild.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
struct CacheFingerprint {
    format: u32,
    date_created: String,
    locale: String,
    num_points: usize,
    zones: usize,
    cache_size: usize,
}

impl CacheFingerprint {
    fn of(cache: &CacheFile) -> Self {
        Self {
            format: SIDECAR_FORMAT,
            date_created: cache.date_created.clone(),
            locale: cache.locale.clone(),
            num_points: cache.num_points,
            zones: cache.zones.len(),
            cache_size: cache.cache_size(),
        }
    }
}

/// Checks a user-supplied index directory before a build is started.
///
/// A non-empty directory without an index was not created by us, so it is
/// rejected rather than wiped: the operator must point `--index-dir` at an
/// empty directory or remove the files first.
pub fn validate_index_dir(dir: &Path) -> Result<(), String> {
    if !dir.exists() {
        return Ok(());
    }
    if !dir.is_dir() {
        return Err(format!("{} is not a directory", dir.display()));
    }
    let mut entries =
        std::fs::read_dir(dir).map_err(|err| format!("cannot read {}: {err}", dir.display()))?;
    if entries.next().is_some() && !dir.join(META_FILE).exists() {
        return Err(format!(
            "{} is not empty and does not contain a forward geocoder index; \
             refusing to touch it (use an empty directory)",
            dir.display()
        ));
    }
    Ok(())
}

/// Creates a fresh index in `dir`, removing a previous index if present.
fn create_index_in_dir(dir: &Path, schema: Schema) -> tantivy::Result<Index> {
    std::fs::create_dir_all(dir)?;
    // `Index::create_in_dir` refuses to overwrite an existing index, and the
    // directory was validated to be either empty or an index.
    if dir.join(META_FILE).exists() {
        std::fs::remove_dir_all(dir)?;
        std::fs::create_dir_all(dir)?;
    }
    Index::create_in_dir(dir, schema)
}

/// Opens the index in `dir` when it was built from the current cache,
/// returning `None` when there is nothing to reuse and a rebuild is needed.
fn open_reusable_index(
    dir: &Path,
    schema: &Schema,
    fingerprint: &CacheFingerprint,
) -> Option<Index> {
    if !dir.join(META_FILE).exists() {
        return None;
    }

    let stored: Option<CacheFingerprint> = std::fs::read(dir.join(SIDECAR_FILE))
        .ok()
        .and_then(|data| serde_json::from_slice(&data).ok());
    match stored {
        Some(stored) if stored == *fingerprint => {}
        Some(stored) => {
            log::info!(
                "forward geocoder: index in {} was built from a different cache \
                 (created {}, {} points; current cache created {}, {} points); rebuilding",
                dir.display(),
                stored.date_created,
                stored.num_points,
                fingerprint.date_created,
                fingerprint.num_points,
            );
            return None;
        }
        None => {
            log::info!(
                "forward geocoder: no usable fingerprint in {}; rebuilding",
                dir.display()
            );
            return None;
        }
    }

    let index = match Index::open_in_dir(dir) {
        Ok(index) => index,
        Err(err) => {
            log::warn!(
                "forward geocoder: cannot open the index in {} ({err}); rebuilding",
                dir.display()
            );
            return None;
        }
    };
    if index.schema() != *schema {
        log::info!(
            "forward geocoder: schema of the index in {} is outdated; rebuilding",
            dir.display()
        );
        return None;
    }
    Some(index)
}

fn register_analyzers(index: &Index, analyzers: &Analyzers) {
    index
        .tokenizers()
        .register(TEXT_ANALYZER, analyzers.text.clone());
    index
        .tokenizers()
        .register(HOUSE_ANALYZER, analyzers.house.clone());
}

fn build_index(
    docs: impl Iterator<Item = IndexedDoc>,
    locale: &str,
    index_dir: Option<&Path>,
    fingerprint: &CacheFingerprint,
) -> tantivy::Result<(Index, Fields, Analyzers)> {
    let (schema, fields) = build_schema();

    let analyzers = build_analyzers(locale);
    log::info!(
        "forward geocoder: locale={locale:?} stemmer_language={:?}",
        analyzers.stemmer_language()
    );

    // A persisted index matching the current cache is reused as-is; anything
    // else means a fresh build.
    if let Some(dir) = index_dir {
        if let Some(mut index) = open_reusable_index(dir, &schema, fingerprint) {
            log::info!("forward geocoder: reusing index from {}", dir.display());
            index.set_default_multithread_executor()?;
            register_analyzers(&index, &analyzers);
            print_usage(&index.reader()?);
            return Ok((index, fields, analyzers));
        }
    }

    let mut index = match index_dir {
        None => Index::create_from_tempdir(schema)?,
        Some(dir) => {
            log::info!("forward geocoder: building index in {}", dir.display());
            create_index_in_dir(dir, schema)?
        }
    };
    index.set_default_multithread_executor()?;
    register_analyzers(&index, &analyzers);

    let mut index_writer: IndexWriter = index.writer(256 * 1024 * 1024)?;

    for d in docs {
        let document = doc!(
            fields.country => d.country,
            fields.region => d.region,
            fields.city => d.city,
            fields.street => d.street,
            fields.house_number => d.house_number,
            fields.name => d.name,
            fields.merged => d.merged_address(),
            fields.geo_type => d.geo_kind as u64,
            fields.cache_location => d.cache_location,
        );
        index_writer.add_document(document)?;
    }

    index_writer.commit()?;
    index_writer.garbage_collect_files().wait()?;
    index_writer.wait_merging_threads()?;

    // Written last: a crash mid-build must not leave a fingerprint that makes
    // the next start reuse a partial index.
    if let Some(dir) = index_dir {
        let data = serde_json::to_vec_pretty(fingerprint).map_err(|err| {
            TantivyError::InvalidArgument(format!("fingerprint serialization failed: {err}"))
        })?;
        std::fs::write(dir.join(SIDECAR_FILE), data)?;
    }

    print_usage(&index.reader()?);

    Ok((index, fields, analyzers))
}

fn print_usage(reader: &IndexReader) {
    let mut fieldnorm_usage: HashMap<String, ByteCount> = HashMap::new();
    let mut fast_field_usage: HashMap<String, ByteCount> = HashMap::new();
    let mut positions_usage: HashMap<String, ByteCount> = HashMap::new();
    let mut postings_usage: HashMap<String, ByteCount> = HashMap::new();
    let mut termdict_usage: HashMap<String, ByteCount> = HashMap::new();
    let mut store_usage = ByteCount::default();

    fn add_map_usage(map: &mut HashMap<String, ByteCount>, usage: &PerFieldSpaceUsage) {
        for field in usage.fields() {
            *map.entry(field.field_name().to_string()).or_default() += field.total();
        }
    }

    for seg in reader.searcher().space_usage().unwrap().segments() {
        add_map_usage(&mut fieldnorm_usage, seg.fieldnorms());
        add_map_usage(&mut fast_field_usage, seg.fast_fields());
        add_map_usage(&mut positions_usage, seg.positions());
        add_map_usage(&mut postings_usage, seg.postings());
        add_map_usage(&mut termdict_usage, seg.termdict());
        store_usage += seg.store().total();
    }

    log::info!("fieldnorm_usage: {:?}", fieldnorm_usage);
    log::info!("fast_field_usage: {:?}", fast_field_usage);
    log::info!("positions_usage: {:?}", positions_usage);
    log::info!("postings_usage: {:?}", postings_usage);
    log::info!("termdict_usage: {:?}", termdict_usage);
    log::info!("store_usage: {:?}", store_usage);
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

#[derive(Clone)]
pub struct ForwardGeocoder {
    index_reader: IndexReader,
    fields: Fields,
    analyzers: Analyzers,
    cache: Arc<CacheFile>,
}

/// Documents materialized (and country-resolved) per parallel batch while the
/// index is built. Bounds peak memory while still using all cores.
const BUILD_CHUNK: usize = 8_192;

/// Point documents, materialized in parallel chunks.
///
/// The country lookup is a polygon containment test against the country border
/// tree — tens of millions of them for a full-country cache — so it is worth
/// parallelizing. Resolving in bounded chunks keeps peak memory flat while the
/// index writer consumes the results sequentially.
fn point_docs<'a>(
    cache: &'a CacheFile,
    geocoder: &'a Geocoder,
) -> impl Iterator<Item = IndexedDoc> + 'a {
    (0..cache.num_points)
        .step_by(BUILD_CHUNK)
        .flat_map(move |start| {
            let end = (start + BUILD_CHUNK).min(cache.num_points);
            (start..end)
                .into_par_iter()
                .map(|i| {
                    let point = cache.point_at(i);
                    IndexedDoc {
                        country: geocoder
                            .country_at(point.lon, point.lat)
                            .unwrap_or_default()
                            .to_string(),
                        region: cache.read_string(point.data.region_id.get()),
                        city: cache.read_string(point.data.city_id.get()),
                        street: cache.read_string(point.data.street_id.get()),
                        house_number: cache.read_string(point.data.house_number_id.get()),
                        name: cache.read_string(point.data.name_id.get()),
                        geo_kind: GeoObjectKind::from_weight(point.data.weight),
                        cache_location: point.location,
                    }
                })
                .collect::<Vec<_>>()
        })
}

/// Zone documents (regions and countries). A zone has no street address — its
/// name is the whole address — so it is indexed under `name` and `merged`
/// alike, with no country.
fn zone_docs(cache: &CacheFile) -> impl Iterator<Item = IndexedDoc> + '_ {
    cache.zones.iter().enumerate().map(|(i, zone)| IndexedDoc {
        country: String::new(),
        region: String::new(),
        city: String::new(),
        street: String::new(),
        house_number: String::new(),
        name: zone.name.clone(),
        geo_kind: GeoObjectKind::Zone,
        cache_location: i as u64,
    })
}

impl ForwardGeocoder {
    pub fn build(
        geocoder: Arc<Geocoder>,
        index_dir: Option<&Path>,
    ) -> tantivy::Result<ForwardGeocoder> {
        if let Some(dir) = index_dir {
            validate_index_dir(dir).map_err(TantivyError::InvalidArgument)?;
        }

        let cache = &geocoder.cache;
        let fingerprint = CacheFingerprint::of(cache);
        let docs = point_docs(cache, &geocoder).chain(zone_docs(cache));

        let (index, fields, analyzers) = build_index(docs, &cache.locale, index_dir, &fingerprint)?;

        Ok(ForwardGeocoder {
            index_reader: index.reader()?,
            fields,
            analyzers,
            cache: geocoder.cache.clone(),
        })
    }
}

pub struct SearchResultItem {
    pub address_string: String,
    pub score: Score,
    pub point: (f64, f64),
    pub geo_type: GeoObjectKind,
    pub multipolygon: Option<geo::MultiPolygon>,
}

/// A clause matching the query token exactly — or, for the token the user is
/// still typing, as a prefix.
fn exact_query(field: Field, text: &str, prefix: bool) -> Box<dyn Query> {
    let term = Term::from_field_text(field, text);
    if prefix && text.chars().count() >= MIN_PREFIX_LEN {
        // Edit distance 0 is a plain prefix match.
        Box::new(FuzzyTermQuery::new_prefix(term, 0, true))
    } else {
        Box::new(TermQuery::new(term, IndexRecordOption::WithFreqs))
    }
}

/// A clause tolerating one edit, so typos and mis-typed inflections still find
/// something. Always paired with a higher-boosted [`exact_query`] so it can
/// only fill gaps, never displace an exact hit.
///
/// `None` for tokens shorter than [`MIN_PREFIX_LEN`]: at one or two characters
/// an edit-distance automaton matches a large share of the term dictionary, so
/// the results are noise and the scan cost is disproportionate (measured: a
/// 2-character query went from 1 ms to 18 ms).
fn fuzzy_query(field: Field, text: &str, prefix: bool) -> Option<Box<dyn Query>> {
    if text.chars().count() < MIN_PREFIX_LEN {
        return None;
    }
    let term = Term::from_field_text(field, text);
    Some(if prefix {
        Box::new(FuzzyTermQuery::new_prefix(term, 1, true))
    } else {
        Box::new(FuzzyTermQuery::new(term, 1, true))
    })
}

/// Build a query for raw user input.
///
/// The input is tokenized with the same analyzers used at index time and turned
/// into `TermQuery`s programmatically. Nothing from the request ever reaches
/// tantivy's query grammar, so `foo:bar`, `[a TO b]`, `-term` and friends are
/// literal text rather than syntax — and can never fail to parse.
fn build_query(fields: &Fields, analyzers: &Analyzers, raw: &str) -> Option<Box<dyn Query>> {
    let raw = raw.trim();
    if raw.is_empty() || raw.len() > MAX_QUERY_LEN {
        return None;
    }

    let segments: Vec<&str> = raw
        .split([',', ';'])
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .take(MAX_SEGMENTS)
        .collect();
    if segments.is_empty() {
        return None;
    }

    let mut house_analyzer = analyzers.house.clone();

    let mut segment_tokens: Vec<Vec<(bool, String)>> = Vec::with_capacity(segments.len());
    for segment in &segments {
        let tokens: Vec<(bool, String)> = tokenize(&mut house_analyzer, segment)
            .into_iter()
            .map(|token| {
                let is_house = token.chars().any(|c| c.is_ascii_digit());
                (is_house, token)
            })
            // Short type words carry no signal and are in no document, so they
            // can only make the query unsatisfiable. Anything with a digit
            // survives regardless of length (house numbers).
            .filter(|(is_house, token)| *is_house || token.chars().count() >= MIN_TOKEN_LEN)
            .collect();
        // A segment that contributes no tokens (punctuation only) carries no
        // constraint; dropping it is friendlier than making the whole query
        // unsatisfiable.
        if !tokens.is_empty() {
            segment_tokens.push(tokens);
        }
    }
    if segment_tokens.is_empty() {
        return None;
    }

    assemble(fields, analyzers, &segment_tokens)
}

/// AND the segments, and AND the tokens within each. A token is free to match
/// any of the text fields (OR across fields).
///
/// On top of that AND query sits one optional clause: the whole query, in
/// order, as a phrase against `merged` (see [`phrase_query`]). It never
/// filters — a document that matches every token but not the phrase is still a
/// hit — it only lifts documents that read as the address the user typed.
fn assemble(
    fields: &Fields,
    analyzers: &Analyzers,
    segment_tokens: &[Vec<(bool, String)>],
) -> Option<Box<dyn Query>> {
    let last_segment = segment_tokens.len() - 1;
    let last_token = segment_tokens[last_segment].len() - 1;

    let mut text_analyzer = analyzers.text.clone();
    let mut segment_queries: Vec<Box<dyn Query>> = Vec::with_capacity(segment_tokens.len());
    // One analyzed form per query token, in query order, whenever every token
    // has exactly one — the terms the merged phrase is built from.
    let mut phrase_terms: Vec<String> = Vec::new();
    let mut phrase_possible = true;

    for (si, tokens) in segment_tokens.iter().enumerate() {
        let mut clauses: Vec<(Occur, Box<dyn Query>)> = Vec::with_capacity(tokens.len());
        for (ti, (is_house, token)) in tokens.iter().enumerate() {
            // The house analyzer gives us the canonical (lowercased, unstemmed)
            // form; re-analyzing that single token with the text analyzer yields
            // the term actually present in the index.
            let text_forms = tokenize(&mut text_analyzer, token);
            // Only the very last token of the query completes as a prefix —
            // that is the one the user is still typing.
            let prefix = si == last_segment && ti == last_token && !*is_house;
            if let Some(clause) = token_clause(fields, token, &text_forms, *is_house, prefix) {
                clauses.push((Occur::Must, clause));
            }
            match text_forms.as_slice() {
                [only] => phrase_terms.push(only.clone()),
                // A token that stems or splits into several terms (or none)
                // has no single position in the merged string, so no phrase is
                // built for the query.
                _ => phrase_possible = false,
            }
        }
        if clauses.is_empty() {
            return None;
        }
        segment_queries.push(Box::new(BooleanQuery::new(clauses)));
    }

    let base: Box<dyn Query> = match segment_queries.as_slice() {
        [only] => only.box_clone(),
        _ => Box::new(BooleanQuery::new(
            segment_queries
                .iter()
                .map(|q| (Occur::Must, q.box_clone()))
                .collect(),
        )),
    };

    let (last_is_house, _) = &segment_tokens[last_segment][last_token];
    let phrase_clauses = if phrase_possible {
        phrase_clauses(fields, &phrase_terms, !last_is_house)
    } else {
        Vec::new()
    };

    if phrase_clauses.is_empty() {
        return Some(base);
    }

    let mut clauses: Vec<(Occur, Box<dyn Query>)> = Vec::with_capacity(phrase_clauses.len() + 1);
    clauses.push((Occur::Must, base));
    // `Should` beside `Must` is optional: every base match is still a hit,
    // phrase or not. The phrase clauses only raise the score.
    clauses.extend(phrase_clauses);
    Some(Box::new(BooleanQuery::new(clauses)))
}

/// The whole query as ordered phrases against `merged`.
///
/// The final term is matched as a prefix only when it is long enough for the
/// prefix automaton to be worth it; `prefix_last` mirrors the token-level rule
/// for the one token the user is still typing. tantivy's prefix phrase has no
/// slop setting, so the prefix tier stays contiguous.
///
/// The completed-phrase case yields two clauses: a strictly contiguous one
/// ([`EXACT_PHRASE_SCORE`]) and one tolerating [`PHRASE_SLOP`] intervening
/// tokens ([`PHRASE_SCORE`]), so `Тверская 12` still boosts a document whose
/// stored street is `Тверская улица`, while the literal query keeps the upper
/// hand. A one-term phrase is skipped — that is just a term query, which the
/// base query already contains.
fn phrase_clauses(
    fields: &Fields,
    terms: &[String],
    prefix_last: bool,
) -> Vec<(Occur, Box<dyn Query>)> {
    if terms.len() < 2 {
        return Vec::new();
    }

    let last_is_prefix = prefix_last
        && terms
            .last()
            .is_some_and(|term| term.chars().count() >= MIN_PREFIX_LEN);

    let merged_terms = || -> Vec<Term> {
        terms
            .iter()
            .map(|term| Term::from_field_text(fields.merged, term))
            .collect()
    };

    if last_is_prefix {
        return vec![(
            Occur::Should,
            Box::new(ConstScoreQuery::new(
                Box::new(PhrasePrefixQuery::new(merged_terms())),
                EXACT_PHRASE_SCORE,
            )),
        )];
    }

    let mut sloppy = PhraseQuery::new(merged_terms());
    sloppy.set_slop(PHRASE_SLOP);

    vec![
        (
            Occur::Should,
            Box::new(ConstScoreQuery::new(
                Box::new(PhraseQuery::new(merged_terms())),
                EXACT_PHRASE_SCORE,
            )),
        ),
        (
            Occur::Should,
            Box::new(ConstScoreQuery::new(Box::new(sloppy), PHRASE_SCORE)),
        ),
    ]
}

/// One query token: it may match any of the text fields, and — when it looks
/// like a house number — the house-number field as well.
///
/// Returns `None` when the token analyzed away to nothing (e.g. it exceeded the
/// length cap), so the caller can drop it instead of adding an empty clause
/// that would match nothing.
fn token_clause(
    fields: &Fields,
    raw_token: &str,
    text_forms: &[String],
    is_house: bool,
    prefix: bool,
) -> Option<Box<dyn Query>> {
    let mut clauses: Vec<(Occur, Box<dyn Query>)> = Vec::with_capacity(text_forms.len() * 8 + 2);

    for form in text_forms {
        for (field, field_boost) in [
            (fields.street, BOOST_STREET),
            (fields.name, BOOST_NAME),
            (fields.city, BOOST_CITY),
            (fields.region, BOOST_REGION),
            (fields.country, BOOST_COUNTRY),
        ] {
            // Both tiers are offered for every token, so a token relaxes
            // independently: one that matches exactly still outranks a sibling
            // that only matched fuzzily. The gap between the two boosts is what
            // keeps an exact hit ahead of a near-miss even though BM25 scores
            // them off different idfs.
            clauses.push((
                Occur::Should,
                Box::new(BoostQuery::new(
                    exact_query(field, form, prefix),
                    field_boost * EXACT_BOOST,
                )),
            ));
            if let Some(fuzzy) = fuzzy_query(field, form, prefix) {
                clauses.push((
                    Occur::Should,
                    Box::new(BoostQuery::new(fuzzy, field_boost * FUZZY_BOOST)),
                ));
            }
        }
    }

    if is_house {
        // Streets carry digits too ("улица 1905 года"), so this is an
        // additional disjunct rather than a replacement. House numbers are
        // never prefix-matched: people type them in full.
        let term = Term::from_field_text(fields.house_number, raw_token);
        clauses.push((
            Occur::Should,
            Box::new(BoostQuery::new(
                Box::new(TermQuery::new(term.clone(), IndexRecordOption::WithFreqs)),
                BOOST_HOUSE * EXACT_BOOST,
            )),
        ));
        if raw_token.chars().count() >= MIN_PREFIX_LEN {
            clauses.push((
                Occur::Should,
                Box::new(BoostQuery::new(
                    Box::new(FuzzyTermQuery::new(term, 1, true)),
                    BOOST_HOUSE * FUZZY_BOOST,
                )),
            ));
        }
    }

    match clauses.len() {
        0 => None,
        1 => clauses.pop().map(|(_, q)| q),
        _ => Some(Box::new(BooleanQuery::new(clauses))),
    }
}

impl ForwardGeocoder {
    pub fn search(
        &self,
        query_text: &str,
        kind: GeocodeKindFilter,
        limit: usize,
    ) -> tantivy::Result<Vec<SearchResultItem>> {
        let limit = limit.clamp(1, MAX_LIMIT);

        let Some(query) = build_query(&self.fields, &self.analyzers, query_text) else {
            return Ok(Vec::new());
        };

        let searcher = self.index_reader.searcher();
        let fetch = limit.saturating_mul(OVERFETCH).clamp(MIN_FETCH, MAX_FETCH);

        // One query carries both tiers: exact/prefix clauses are boosted well
        // above the fuzzy ones, so an exact hit always outranks a near-miss
        // while near-misses still surface when nothing better exists.
        let hits = self.collect(&searcher, query.as_ref(), kind, fetch)?;

        self.collapse(&searcher, hits, limit)
    }

    fn collect(
        &self,
        searcher: &Searcher,
        query: &dyn Query,
        kind: GeocodeKindFilter,
        fetch: usize,
    ) -> tantivy::Result<Vec<(Score, DocAddress)>> {
        // `FilterCollector` runs the predicate on the fast-field value, so the
        // kind filter is applied during collection rather than after it.
        let collector = FilterCollector::new(
            GEO_TYPE_FIELD.to_string(),
            move |value: u64| {
                GeoObjectKind::try_from(value)
                    .map(|obj_type| kind.matches(obj_type))
                    .unwrap_or(false)
            },
            TopDocs::with_limit(fetch).order_by_score(),
        );
        searcher.search(query, &collector)
    }

    /// Collapse the over-fetched hits to one entry per distinct address.
    ///
    /// The index stores a document per point, so a long road is resampled every
    /// 150 m and an area is Poisson-filled — without this, one street fills the
    /// whole result page with the same address.
    fn collapse(
        &self,
        searcher: &Searcher,
        hits: Vec<(Score, DocAddress)>,
        limit: usize,
    ) -> tantivy::Result<Vec<SearchResultItem>> {
        let mut seen: HashMap<String, usize> = HashMap::new();
        let mut kept: Vec<(Score, SearchResultItem)> = Vec::new();

        for (score, address) in hits {
            let document: TantivyDocument = searcher.doc(address)?;
            let (key, item) = self.materialize(&document, score)?;
            match seen.get(&key) {
                Some(&index) => {
                    if score > kept[index].0 {
                        kept[index] = (score, item);
                    }
                }
                None => {
                    seen.insert(key, kept.len());
                    kept.push((score, item));
                }
            }
        }

        kept.sort_by(|a, b| {
            b.0.total_cmp(&a.0)
                .then_with(|| kind_rank(b.1.geo_type).cmp(&kind_rank(a.1.geo_type)))
                .then_with(|| a.1.address_string.cmp(&b.1.address_string))
        });
        kept.truncate(limit);

        Ok(kept.into_iter().map(|(_, item)| item).collect())
    }

    /// Turn a stored document into a result, plus the key it collapses under.
    fn materialize(
        &self,
        document: &TantivyDocument,
        score: Score,
    ) -> tantivy::Result<(String, SearchResultItem)> {
        let text = |field: Field| -> &str {
            document
                .get_first(field)
                .and_then(|value| value.as_str())
                .unwrap_or("")
                .trim()
        };

        let geo_type = GeoObjectKind::try_from(
            document
                .get_first(self.fields.geo_type)
                .and_then(|value| value.as_u64())
                .ok_or_else(|| {
                    TantivyError::InvalidArgument("document is missing geo_type".into())
                })?,
        )?;

        let cache_location = document
            .get_first(self.fields.cache_location)
            .and_then(|value| value.as_u64())
            .ok_or_else(|| {
                TantivyError::InvalidArgument("document is missing cache_location".into())
            })?;
        let is_zone = geo_type == GeoObjectKind::Zone;

        let (lat, lon, multipolygon) = if is_zone {
            let zone = self
                .cache
                .zones
                .get(cache_location as usize)
                .ok_or_else(|| {
                    TantivyError::InvalidArgument(format!(
                        "zone index {cache_location} is out of range for this cache"
                    ))
                })?;
            // The cache holds no coordinate for a zone, so its centroid is
            // recomputed here — the same `centroid()` on the same polygon the
            // builder used, so the value is unchanged. O(vertices), but dwarfed
            // by the polygon clone the response already needs.
            let centroid = zone.polygon.centroid().ok_or_else(|| {
                TantivyError::InvalidArgument("zone polygon has no centroid".into())
            })?;
            (centroid.y(), centroid.x(), Some(zone.polygon.clone()))
        } else {
            let (lon, lat) = self.cache.read_coord(cache_location as usize);
            (lat.get(), lon.get(), None)
        };

        // Empty strings are stored as-is (tantivy's add_text has no empty check),
        // so a road with no `name` would otherwise render as "…, Ленина, , ".
        let parts: Vec<&str> = [
            text(self.fields.region),
            text(self.fields.city),
            text(self.fields.street),
            text(self.fields.house_number),
            text(self.fields.name),
        ]
        .into_iter()
        .filter(|part| !part.is_empty())
        .collect();

        let address_string = parts.join(", ");

        let mut key = String::with_capacity(address_string.len() + 8);
        key.push_str(&(geo_type as u64).to_string());
        for part in &parts {
            key.push('\u{1}');
            key.push_str(&part.to_lowercase());
        }
        if is_zone {
            // Two same-named zones must stay distinct results.
            key.push('\u{1}');
            key.push_str(&cache_location.to_string());
        }

        Ok((
            key,
            SearchResultItem {
                address_string,
                score,
                point: (lat, lon),
                geo_type,
                multipolygon,
            },
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn indexed_doc(
        country: &str,
        city: &str,
        street: &str,
        house_number: &str,
        name: &str,
        location: u64,
    ) -> IndexedDoc {
        IndexedDoc {
            country: country.to_string(),
            region: String::new(),
            city: city.to_string(),
            street: street.to_string(),
            house_number: house_number.to_string(),
            name: name.to_string(),
            geo_kind: GeoObjectKind::Building,
            cache_location: location,
        }
    }

    fn build_test_index(docs: Vec<IndexedDoc>) -> (Index, Fields, Analyzers) {
        let fingerprint = CacheFingerprint {
            format: SIDECAR_FORMAT,
            date_created: "test".to_string(),
            locale: String::new(),
            num_points: docs.len(),
            zones: 0,
            cache_size: 0,
        };
        build_index(docs.into_iter(), "", None, &fingerprint).unwrap()
    }

    /// Search and render the stored address fields, in ranked order.
    fn search(
        index: &Index,
        fields: &Fields,
        analyzers: &Analyzers,
        query_text: &str,
    ) -> Vec<String> {
        search_scored(index, fields, analyzers, query_text)
            .into_iter()
            .map(|(_, address)| address)
            .collect()
    }

    /// Search and return `(score, rendered address)` pairs, in ranked order.
    fn search_scored(
        index: &Index,
        fields: &Fields,
        analyzers: &Analyzers,
        query_text: &str,
    ) -> Vec<(Score, String)> {
        let query = build_query(fields, analyzers, query_text).expect("query must build");
        let searcher = index.reader().unwrap().searcher();
        searcher
            .search(query.as_ref(), &TopDocs::with_limit(10).order_by_score())
            .unwrap()
            .into_iter()
            .map(|(score, address)| {
                let document: TantivyDocument = searcher.doc(address).unwrap();
                (score, render_address(&document, *fields))
            })
            .collect()
    }

    /// Render the stored address fields the same way `materialize` does.
    fn render_address(document: &TantivyDocument, fields: Fields) -> String {
        let text = |field: Field| {
            document
                .get_first(field)
                .and_then(|value| value.as_str())
                .unwrap_or("")
                .to_string()
        };
        [
            text(fields.country),
            text(fields.city),
            text(fields.street),
            text(fields.house_number),
            text(fields.name),
        ]
        .into_iter()
        .filter(|part| !part.is_empty())
        .collect::<Vec<_>>()
        .join(", ")
    }

    #[test]
    fn merged_address_skips_empty_parts() {
        let doc = indexed_doc("Russia", "", "High Street", "12", "", 0);
        assert_eq!(doc.merged_address(), "Russia, High Street, 12");
    }

    #[test]
    fn phrase_outranks_the_same_tokens_in_another_order() {
        let in_order = indexed_doc("Russia", "London", "High Street", "12", "", 0);
        let reordered = indexed_doc("Russia", "London", "", "12", "High Street", 1);
        let (index, fields, analyzers) = build_test_index(vec![in_order, reordered]);

        let results = search(
            &index,
            &fields,
            &analyzers,
            "russia, london, high street, 12",
        );

        assert_eq!(
            results.first().map(String::as_str),
            Some("Russia, London, High Street, 12"),
            "the in-order phrase must outrank the reordered match"
        );
        assert!(
            results.contains(&"Russia, London, 12, High Street".to_string()),
            "a document matching every token but not the phrase must still be returned"
        );
    }

    #[test]
    fn sloppy_phrase_covers_one_gap_and_exact_still_wins() {
        // The query omits "street", so in this document "street" sits between
        // "high" and "12": only the slop tier can match it.
        let one_gap = indexed_doc("Russia", "London", "High Street", "12", "", 0);
        // Nothing intervenes here, so the same query is a contiguous match.
        let contiguous = indexed_doc("Russia", "London", "High", "12", "Street", 1);
        let (index, fields, analyzers) = build_test_index(vec![one_gap, contiguous]);

        let results = search_scored(&index, &fields, &analyzers, "russia london high 12");

        let (contiguous_score, contiguous_address) = &results[0];
        assert_eq!(contiguous_address, "Russia, London, High, 12, Street");
        assert!(
            *contiguous_score >= PHRASE_SCORE + EXACT_PHRASE_SCORE,
            "the contiguous match must carry both phrase tiers, got {contiguous_score}"
        );
        let (gap_score, _) = results
            .iter()
            .find(|(_, address)| address == "Russia, London, High Street, 12")
            .expect("the one-gap document must be returned");
        assert!(
            *gap_score >= PHRASE_SCORE,
            "the one-gap match must carry the slop tier, got {gap_score}"
        );
        assert!(
            *contiguous_score > *gap_score,
            "the contiguous match must outrank the one-gap match"
        );
    }

    #[test]
    fn phrase_matches_a_prefix_on_the_last_token() {
        let doc = indexed_doc("Russia", "London", "High Street", "", "", 0);
        let (index, fields, analyzers) = build_test_index(vec![doc]);

        let results = search(&index, &fields, &analyzers, "russia london high stre");

        assert_eq!(results, vec!["Russia, London, High Street".to_string()]);
    }

    #[test]
    fn country_field_is_searchable() {
        let with_country = indexed_doc("Russia", "Moscow", "Tverskaya", "12", "", 0);
        let without_country = indexed_doc("", "Moscow", "Tverskaya", "12", "", 1);
        let (index, fields, analyzers) = build_test_index(vec![with_country, without_country]);

        let results = search(&index, &fields, &analyzers, "russia");

        assert_eq!(results, vec!["Russia, Moscow, Tverskaya, 12".to_string()]);
    }
}
