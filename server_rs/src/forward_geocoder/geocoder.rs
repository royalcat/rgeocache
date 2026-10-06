use std::collections::HashMap;
use std::path::Path;
use std::str::FromStr;
use std::sync::Arc;

use rayon::prelude::*;
use serde::{Deserialize, Serialize};
use strum::EnumString;
use tantivy::collector::{FilterCollector, TopDocs};
use tantivy::fieldnorm::FieldNormReader;
use tantivy::query::{
    BooleanQuery, BoostQuery, ConstScoreQuery, FuzzyTermQuery, Occur, PhrasePrefixQuery,
    PhraseQuery, Query, TermQuery,
};
use tantivy::schema::*;
use tantivy::space_usage::PerFieldSpaceUsage;
use tantivy::{
    doc, DocAddress, DocId, Index, IndexReader, IndexWriter, Score, Searcher, SegmentReader,
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
/// they carry a digit or prefix another token.
///
/// This is what makes abbreviated addresses work: the type words people type
/// ("г", "ул", "д", "кв") are short and appear in no document, and under the AND
/// semantics below a single token that matches nothing makes the whole query
/// unsatisfiable. Dropping them needs no vocabulary, unlike a stopword list.
///
/// The digit exemption is load-bearing — house numbers are short, and a plain
/// length cut would discard `12` and `5` along with the type words.
///
/// Longer tokens that exist in no document are dropped too (see
/// [`token_exists`]), which covers `город Москва` and `дом 12`; unlike the
/// length rule that needs the term dictionary at query-build time.
const MIN_TOKEN_LEN: usize = 3;

/// Longest accepted query string. Longer is rejected by the HTTP handler.
pub const MAX_QUERY_LEN: usize = 256;

pub const DEFAULT_LIMIT: usize = 10;
pub const MAX_LIMIT: usize = 100;

// The index holds one document per point, so a common street name matches many
// near-identical rows. Over-fetch, collapse, then truncate.
const OVERFETCH: usize = 10;
const MIN_FETCH: usize = 50;
const MAX_FETCH: usize = 500;

/// Cap on distinct terms collected while scanning term dictionaries for
/// suggestions; bounds the work for a very short prefix.
const SUGGESTION_SCAN_CAP: usize = 10_000;

/// Tie-break for equal BM25 scores.
///
/// A single-term query scores every document matching the same term in the same
/// field identically (`score = boost × idf`), so ties are common and the order
/// within them would otherwise be arbitrary. A specific address is a more
/// useful hit than the road that contains it, which in turn beats the
/// region/country polygon. Also makes the response deterministic.
fn kind_rank(obj_type: GeoObjectKind) -> u8 {
    match obj_type {
        GeoObjectKind::Building => 3,
        GeoObjectKind::Road => 2,
        GeoObjectKind::Area => 1,
        GeoObjectKind::Zone => 0,
    }
}

/// A distinct term suggestion and its approximate document frequency.
#[derive(Debug, Clone, serde::Serialize)]
pub struct Suggestion {
    pub text: String,
    pub doc_freq: u64,
}

fn tokenize(analyzer: &mut TextAnalyzer, text: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut stream = analyzer.token_stream(text);
    while stream.advance() {
        out.push(stream.token().text.clone());
    }
    out
}

/// Smallest string greater than every string starting with `prefix`.
///
/// Used as the exclusive upper bound of the term-dictionary range scan. Falls
/// back to `prefix` itself for an all-max-codepoint prefix (an empty range).
fn prefix_upper_bound(prefix: &str) -> String {
    let mut chars: Vec<char> = prefix.chars().collect();
    while let Some(last) = chars.pop() {
        if let Some(next) = char::from_u32(last as u32 + 1) {
            chars.push(next);
            return chars.into_iter().collect();
        }
    }
    prefix.to_string()
}

/// Core of [`ForwardGeocoder::suggest`], split out so it can be tested against
/// a synthetic index.
fn suggest_terms(
    searcher: &Searcher,
    fields: &Fields,
    analyzers: &Analyzers,
    input: &str,
    limit: usize,
) -> tantivy::Result<Vec<Suggestion>> {
    let input = input.trim();
    if input.is_empty() {
        return Ok(Vec::new());
    }

    // The suggest field is analyzed without a stemmer, so the query side must
    // not stem either: the user is typing a prefix of the word they will see.
    let mut analyzer = analyzers.house.clone();
    let forms = tokenize(&mut analyzer, input);
    let Some(prefix) = forms.last() else {
        return Ok(Vec::new());
    };

    let upper = prefix_upper_bound(prefix);
    let mut counts: HashMap<String, u64> = HashMap::new();

    'segments: for segment_reader in searcher.segment_readers() {
        let inverted = segment_reader.inverted_index(fields.suggest)?;
        let mut stream = inverted
            .terms()
            .range()
            .ge(prefix.as_str())
            .lt(upper.as_str())
            .into_stream()?;
        while let Some((key, info)) = stream.next() {
            let text = String::from_utf8_lossy(key).into_owned();
            *counts.entry(text).or_default() += info.doc_freq as u64;
            if counts.len() >= SUGGESTION_SCAN_CAP {
                break 'segments;
            }
        }
    }

    let mut suggestions: Vec<Suggestion> = counts
        .into_iter()
        .map(|(text, doc_freq)| Suggestion { text, doc_freq })
        .collect();
    suggestions.sort_by(|a, b| {
        b.doc_freq
            .cmp(&a.doc_freq)
            .then_with(|| a.text.cmp(&b.text))
    });
    suggestions.truncate(limit.clamp(1, MAX_LIMIT));
    Ok(suggestions)
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
    #[strum(serialize = "area", serialize = "a")]
    Area = 4,
}

impl GeoObjectKind {
    /// Derive the object kind from a point's cache record.
    ///
    /// `geo_type` is the explicit kind written by the generator; when it is
    /// absent (legacy 21-byte records widen to 0) or unrecognised, fall back to
    /// the weight-derived kind.
    pub fn from_cache(geo_type: u8, weight: u8) -> GeoObjectKind {
        // Values match cachesaver/model.GeoObjectType on the Go side.
        const CACHE_BUILDING: u8 = 1;
        const CACHE_ROAD: u8 = 2;
        const CACHE_AREA: u8 = 3;
        match geo_type {
            CACHE_BUILDING => GeoObjectKind::Building,
            CACHE_ROAD => GeoObjectKind::Road,
            CACHE_AREA => GeoObjectKind::Area,
            _ => GeoObjectKind::from_weight(weight),
        }
    }

    /// Fallback used when the cache carries no explicit geo type (legacy
    /// records). `weight` is a lossy proxy: 5 is a road (highways are
    /// resampled), 3/2 are the industrial/protected area sub-kinds, everything
    /// else is a building.
    pub fn from_weight(weight: u8) -> GeoObjectKind {
        match weight {
            5 => GeoObjectKind::Road,
            3 | 2 => GeoObjectKind::Area,
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
            4 => Ok(GeoObjectKind::Area),
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
    areas: bool,
}

impl Default for GeocodeKindFilter {
    fn default() -> Self {
        GeocodeKindFilter {
            zones: true,
            buildings: true,
            roads: true,
            areas: true,
        }
    }
}

impl From<&str> for GeocodeKindFilter {
    fn from(s: &str) -> Self {
        let mut filter = GeocodeKindFilter {
            zones: false,
            buildings: false,
            roads: false,
            areas: false,
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
                Ok(GeoObjectKind::Area) => filter.areas = true,
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
            GeoObjectKind::Area => self.areas,
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
    /// Raw house number, indexed and queryable in the form the cache stores it.
    house_number: Field,
    /// Canonicalized house number (aliases expanded, separators removed — see
    /// [`canonical_house`]), so `12 к 1` and `12к1` index to the same term.
    house_normalized: Field,
    name: Field,
    /// Every address part joined in the order the phrase query expects. Not
    /// stored — it exists only to be matched.
    merged: Field,
    /// Surface (unstemmed, lowercased) address tokens, indexed with the house
    /// analyzer and read only by the autocomplete term scan. Without it the
    /// term dictionary of the text fields yields stems (`твер`, `тверск`)
    /// instead of the words a user recognizes.
    suggest: Field,
    geo_type: Field,
    /// Where the document's geometry lives in the cache — see [`IndexedDoc`].
    /// `geo_type` says how to read it. Text fields are never stored: the
    /// rendered address is resolved from the mmap'd cache through this value.
    cache_location: Field,
    /// Weighted point count of the document's street/name, an index-time
    /// popularity signal — see [`Popularity`].
    popularity: Field,
}

fn build_schema() -> (Schema, Fields) {
    let mut schema_builder = Schema::builder();

    // Text fields are indexed, never stored: the address a hit renders is
    // resolved from the cache at query time, so storing a second copy of every
    // string in the doc store only costs space and decompression.
    let text_options = |analyzer: &str| {
        TextOptions::default().set_indexing_options(
            TextFieldIndexing::default()
                .set_tokenizer(analyzer)
                .set_index_option(IndexRecordOption::WithFreqs),
        )
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
        house_normalized: schema_builder
            .add_text_field("house_normalized", text_options(HOUSE_ANALYZER)),
        name: schema_builder.add_text_field("name", text_options(TEXT_ANALYZER)),
        merged: schema_builder.add_text_field("merged", merged_options),
        // Suggestions only need document frequency, not term frequencies or
        // positions.
        suggest: schema_builder.add_text_field(
            "suggest",
            TextOptions::default().set_indexing_options(
                TextFieldIndexing::default()
                    .set_tokenizer(HOUSE_ANALYZER)
                    .set_index_option(IndexRecordOption::Basic),
            ),
        ),
        geo_type: schema_builder.add_u64_field(GEO_TYPE_FIELD, FAST),
        cache_location: schema_builder.add_u64_field("cache_location", FAST),
        popularity: schema_builder.add_u64_field("popularity", FAST),
    };

    (schema_builder.build(), fields)
}

// ---------------------------------------------------------------------------
// Index construction
// ---------------------------------------------------------------------------

/// One document's worth of indexed data, decoupled from [`CacheFile`] so the
/// indexer can be exercised against synthetic fixtures in tests.
#[derive(Clone, Debug)]
struct IndexedDoc {
    country: String,
    region: String,
    city: String,
    street: String,
    house_number: String,
    /// Canonicalized house number indexed beside the raw one — see
    /// [`canonical_house`].
    house_normalized: String,
    name: String,
    geo_kind: GeoObjectKind,
    /// Index-time popularity used to break BM25 ties — see [`Popularity`].
    popularity: u64,
    /// Where this document's geometry lives in the cache; `geo_type` says how
    /// to read it:
    ///
    /// - point documents: the sorted KD-tree position, resolved through
    ///   `CacheFile::read_coord` and the point's string IDs;
    /// - zone documents: an index into `CacheFile::zones`.
    ///
    /// Coordinates and address strings are deliberately *not* stored: they are
    /// high-entropy and compress to nothing in the doc store, while the cache
    /// already holds them.
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

    /// The string indexed into `suggest`: every address part, in render order,
    /// so the autocomplete term scan sees real words rather than stems.
    fn suggest_text(&self) -> String {
        [
            self.country.as_str(),
            self.region.as_str(),
            self.city.as_str(),
            self.street.as_str(),
            self.name.as_str(),
        ]
        .into_iter()
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .collect::<Vec<_>>()
        .join(", ")
    }
}

/// House-number type words that mean the same thing in Russian addresses.
///
/// Both sides of the index — the stored value and the query — are canonicalized
/// through this table, so `12 корпус 1`, `12 к 1` and `12к1` all meet at
/// `12к1`.
const HOUSE_ALIASES: &[(&str, &str)] = &[
    ("к", "к"),
    ("корп", "к"),
    ("корпус", "к"),
    ("с", "с"),
    ("стр", "с"),
    ("строение", "с"),
    ("л", "л"),
    ("лит", "л"),
    ("литер", "л"),
    ("литера", "л"),
];

/// Canonical form of a house number: lowercased, alias words shortened, and
/// whitespace removed — `12 корпус 1` → `12к1`.
///
/// Only whitespace is removed. Other separators are preserved (`12-1` stays
/// `12-1`) because the tokenizer already splits on them, and collapsing them
/// would make `12-1` collide with `121`.
fn canonical_house(raw: &str) -> String {
    raw.to_lowercase()
        .split_whitespace()
        .map(|word| {
            HOUSE_ALIASES
                .iter()
                .find(|(alias, _)| *alias == word)
                .map(|(_, canonical)| *canonical)
                .unwrap_or(word)
        })
        .collect()
}

/// Weighted point counts backing the popularity fast field.
///
/// BM25 scores every document matching the same term identically, and idf is
/// anti-popularity, so without an external signal autocomplete ranks a small
/// town's street above a capital's. Summing each point's generator weight per
/// street and per name gives a cheap "how big is this street/place" proxy.
struct Popularity {
    street: Vec<u64>,
    name: Vec<u64>,
}

impl Popularity {
    fn of(&self, street_id: u32, name_id: u32) -> u64 {
        self.street.get(street_id as usize).copied().unwrap_or(0)
            + self.name.get(name_id as usize).copied().unwrap_or(0)
    }
}

/// Chunk size for the popularity pass, bounding the per-thread accumulator
/// memory against the string table size.
const POPULARITY_CHUNK: usize = 1 << 20;

/// Sum point weights per street and name in one parallel pass.
///
/// The accumulators are dense vectors indexed by string id (the string table of
/// the full-country cache holds ~600k entries, so a pair of vectors is a few
/// MB), which makes the pass allocation-free apart from the chunk merges.
fn compute_popularity(cache: &CacheFile) -> Popularity {
    let num_strings = cache.strings_index.len();

    // A chunk accumulator is merged into the running total as soon as it is
    // built, so only a few are alive at a time instead of one per chunk.
    let chunk_popularity = |start: usize| -> Popularity {
        let end = (start + POPULARITY_CHUNK).min(cache.num_points);
        let mut chunk = Popularity {
            street: vec![0u64; num_strings],
            name: vec![0u64; num_strings],
        };
        for i in start..end {
            let point = cache.point_at(i);
            let weight = point.data.weight as u64;
            // String id 0 means "empty": its bucket must stay zero, otherwise
            // every document with an empty street or name inherits the weight
            // of the whole country's empty-<field> points.
            let street_id = point.data.street_id.get() as usize;
            if street_id != 0 {
                chunk.street[street_id] += weight;
            }
            let name_id = point.data.name_id.get() as usize;
            if name_id != 0 {
                chunk.name[name_id] += weight;
            }
        }
        chunk
    };

    let merge = |mut total: Popularity, chunk: Popularity| -> Popularity {
        for (total, part) in total.street.iter_mut().zip(chunk.street) {
            *total += part;
        }
        for (total, part) in total.name.iter_mut().zip(chunk.name) {
            *total += part;
        }
        total
    };

    let zero = || Popularity {
        street: vec![0u64; num_strings],
        name: vec![0u64; num_strings],
    };

    (0..cache.num_points)
        .into_par_iter()
        .step_by(POPULARITY_CHUNK)
        .map(chunk_popularity)
        .reduce(zero, merge)
}

/// Name of the tantivy meta file, the marker of an index directory.
const META_FILE: &str = "meta.json";

/// Sidecar file recording which cache a persisted index was built from.
const SIDECAR_FILE: &str = "rgeocache-fgeocode.json";

/// Bump to invalidate every persisted index.
const SIDECAR_FORMAT: u32 = 2;

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
) -> tantivy::Result<(Index, Fields, Analyzers, IndexMode)> {
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
            return Ok((index, fields, analyzers, IndexMode::Reused));
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

    let mut indexed = 0usize;
    for d in docs {
        let document = doc!(
            fields.country => d.country,
            fields.region => d.region,
            fields.city => d.city,
            fields.street => d.street,
            fields.house_number => d.house_number,
            fields.house_normalized => d.house_normalized,
            fields.name => d.name,
            fields.merged => d.merged_address(),
            fields.suggest => d.suggest_text(),
            fields.geo_type => d.geo_kind as u64,
            fields.cache_location => d.cache_location,
            fields.popularity => d.popularity,
        );
        index_writer.add_document(document)?;

        indexed += 1;
        if indexed.is_multiple_of(1_000_000) {
            log::info!("forward geocoder: indexed {indexed} documents");
        }
    }
    log::info!("forward geocoder: indexed {indexed} documents total");

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

    Ok((index, fields, analyzers, IndexMode::Built))
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
    /// The reverse geocoder, kept for the mmap'd cache (address strings,
    /// coordinates, zones) and for country resolution at materialization time.
    geocoder: Arc<Geocoder>,
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
    popularity: &'a Popularity,
) -> impl Iterator<Item = IndexedDoc> + 'a {
    (0..cache.num_points)
        .step_by(BUILD_CHUNK)
        .flat_map(move |start| {
            let end = (start + BUILD_CHUNK).min(cache.num_points);
            (start..end)
                .into_par_iter()
                .map(|i| {
                    let point = cache.point_at(i);
                    let house_number = cache.read_string(point.data.house_number_id.get());
                    IndexedDoc {
                        country: geocoder
                            .country_at(point.lon, point.lat)
                            .unwrap_or_default()
                            .to_string(),
                        region: cache.read_string(point.data.region_id.get()),
                        city: cache.read_string(point.data.city_id.get()),
                        street: cache.read_string(point.data.street_id.get()),
                        house_normalized: canonical_house(&house_number),
                        house_number,
                        name: cache.read_string(point.data.name_id.get()),
                        geo_kind: GeoObjectKind::from_cache(point.data.geo_type, point.data.weight),
                        popularity: popularity
                            .of(point.data.street_id.get(), point.data.name_id.get()),
                        cache_location: point.location,
                    }
                })
                .collect::<Vec<_>>()
        })
}

/// Zone documents (regions and countries). A zone has no street address — its
/// name is the whole address — so it is indexed under `name` and `merged`
/// alike, with no country and no popularity.
fn zone_docs(cache: &CacheFile) -> impl Iterator<Item = IndexedDoc> + '_ {
    cache.zones.iter().enumerate().map(|(i, zone)| IndexedDoc {
        country: String::new(),
        region: String::new(),
        city: String::new(),
        street: String::new(),
        house_number: String::new(),
        house_normalized: String::new(),
        name: zone.name.clone(),
        geo_kind: GeoObjectKind::Zone,
        popularity: 0,
        cache_location: i as u64,
    })
}

/// Whether a persisted index was reused or freshly built; surfaced as a metric.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum IndexMode {
    Built,
    Reused,
}

impl ForwardGeocoder {
    pub fn build(
        geocoder: Arc<Geocoder>,
        index_dir: Option<&Path>,
    ) -> tantivy::Result<(ForwardGeocoder, IndexMode)> {
        if let Some(dir) = index_dir {
            validate_index_dir(dir).map_err(TantivyError::InvalidArgument)?;
        }

        let cache = &geocoder.cache;
        let fingerprint = CacheFingerprint::of(cache);
        let popularity = compute_popularity(cache);
        let docs = point_docs(cache, &geocoder, &popularity).chain(zone_docs(cache));

        let (index, fields, analyzers, mode) =
            build_index(docs, &cache.locale, index_dir, &fingerprint)?;

        Ok((
            ForwardGeocoder {
                index_reader: index.reader()?,
                fields,
                analyzers,
                geocoder,
            },
            mode,
        ))
    }
}

pub struct SearchResultItem {
    pub address_string: String,
    pub score: Score,
    pub point: (f64, f64),
    pub geo_type: GeoObjectKind,
    /// Resolved from the country border tree (empty for zones).
    pub country: String,
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

/// One analyzed query token, plus the segment-level canonical house form.
struct QueryToken {
    /// The token in its house-analyzer form (lowercased, unstemmed).
    text: String,
    /// The token re-analyzed with the text analyzer — the forms actually
    /// present in the index.
    text_forms: Vec<String>,
    /// Whether the raw token carries a digit (a house number, or a street name
    /// like "1905 года" — the caller offers both interpretations).
    is_house: bool,
}

/// One comma/semicolon separated address part.
struct Segment {
    tokens: Vec<QueryToken>,
    /// Canonical house form when the segment mixes a digit with a house type
    /// word (`12 к 1` → `12к1`); matched against `house_normalized` so the
    /// alias spelling and the compact spelling meet.
    canonical_house: Option<String>,
}

fn token_has_digit(token: &str) -> bool {
    token.chars().any(|c| c.is_ascii_digit())
}

fn is_house_alias(token: &str) -> bool {
    HOUSE_ALIASES.iter().any(|(alias, _)| *alias == token)
}

/// Canonical form of one token: alias words map to their short form, anything
/// else passes through unchanged.
fn canonical_token(token: &str) -> &str {
    HOUSE_ALIASES
        .iter()
        .find(|(alias, _)| *alias == token)
        .map(|(_, canonical)| *canonical)
        .unwrap_or(token)
}

/// Whether any field of the index contains any analyzed form of this token.
///
/// A token that matches nothing can only make the whole AND query
/// unsatisfiable — that is what makes `город Москва` or `дом 12` work without a
/// stopword list. The check is language-independent: it asks the term
/// dictionary, not a vocabulary.
fn token_exists(searcher: &Searcher, fields: &Fields, token: &QueryToken) -> bool {
    for form in &token.text_forms {
        for field in [
            fields.country,
            fields.region,
            fields.city,
            fields.street,
            fields.name,
        ] {
            let term = Term::from_field_text(field, form);
            if searcher.doc_freq(&term).unwrap_or(0) > 0 {
                return true;
            }
        }
    }
    if token.is_house {
        for field in [fields.house_number, fields.house_normalized] {
            let term = Term::from_field_text(field, &token.text);
            if searcher.doc_freq(&term).unwrap_or(0) > 0 {
                return true;
            }
        }
    }
    false
}

/// Build a query for raw user input.
///
/// The input is tokenized with the same analyzers used at index time and turned
/// into `TermQuery`s programmatically. Nothing from the request ever reaches
/// tantivy's query grammar, so `foo:bar`, `[a TO b]`, `-term` and friends are
/// literal text rather than syntax — and can never fail to parse.
///
/// Tokens whose analyzed forms exist in no field are dropped unless they are
/// the token the user is still typing (the last one), which may still complete
/// as a prefix. This is the general form of the short-type-word rule: it needs
/// no vocabulary and works for every language.
///
/// The returned [`BuiltQuery`] also reports how many tokens the phrase covers,
/// which the collector turns into the whole-address tier.
fn build_query(
    fields: &Fields,
    analyzers: &Analyzers,
    searcher: &Searcher,
    raw: &str,
) -> Option<BuiltQuery> {
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
    let mut text_analyzer = analyzers.text.clone();

    // First pass: tokenize and classify, keeping short tokens for now so a
    // segment's canonical house form is built from the full token list.
    let mut raw_segments: Vec<(Vec<String>, Option<String>)> = Vec::with_capacity(segments.len());
    for segment in &segments {
        let tokens = tokenize(&mut house_analyzer, segment);
        let has_digit = tokens.iter().any(|token| token_has_digit(token));
        let has_alias = tokens.iter().any(|token| is_house_alias(token));
        let canonical_house = (has_digit && has_alias).then(|| {
            tokens
                .iter()
                .filter(|token| token_has_digit(token) || is_house_alias(token))
                .map(|token| canonical_token(token))
                .collect::<String>()
        });
        raw_segments.push((tokens, canonical_house));
    }

    // Second pass: drop the type words that carry no signal, then the tokens
    // that exist nowhere in the index. The last token is always kept: it is the
    // one still being typed and may match as a prefix.
    let total_segments = raw_segments.len();
    let mut segments_out: Vec<Segment> = Vec::with_capacity(total_segments);
    for (si, (tokens, canonical_house)) in raw_segments.into_iter().enumerate() {
        let mut kept: Vec<QueryToken> = Vec::with_capacity(tokens.len());
        for token in tokens {
            let is_house = token_has_digit(&token);
            // Short type words carry no signal and are in no document, so they
            // can only make the query unsatisfiable. Anything with a digit
            // survives regardless of length (house numbers).
            if !is_house && token.chars().count() < MIN_TOKEN_LEN {
                continue;
            }
            let text_forms = tokenize(&mut text_analyzer, &token);
            kept.push(QueryToken {
                text: token,
                text_forms,
                is_house,
            });
        }

        let is_last_segment = si == total_segments - 1;
        let kept_len = kept.len();
        let mut tokens_out: Vec<QueryToken> = Vec::with_capacity(kept_len);
        for (index, token) in kept.into_iter().enumerate() {
            // Only the very last token of the whole query is exempt from the
            // existence check; everything else must be able to constrain.
            let is_typing_token = is_last_segment && index + 1 == kept_len;
            if is_typing_token || token_exists(searcher, fields, &token) {
                tokens_out.push(token);
            }
        }

        if !tokens_out.is_empty() {
            segments_out.push(Segment {
                tokens: tokens_out,
                canonical_house,
            });
        }
    }
    if segments_out.is_empty() {
        return None;
    }

    assemble(fields, &segments_out)
}

/// Index of the token that gets prefix (autocomplete) matching.
///
/// Normally that is the very last token. When the last token is a house number
/// the user has already typed in full — `тверск 12` — the prefix moves to the
/// preceding text token, which is the one still being completed.
fn prefix_target(segments: &[Segment]) -> Option<(usize, usize)> {
    let last_segment = segments.len() - 1;
    let last_token = segments[last_segment].tokens.len() - 1;
    if !segments[last_segment].tokens[last_token].is_house {
        return Some((last_segment, last_token));
    }
    for (si, segment) in segments.iter().enumerate().rev() {
        for (ti, token) in segment.tokens.iter().enumerate().rev() {
            if !token.is_house {
                return Some((si, ti));
            }
        }
    }
    None
}

/// A built free-text query plus the number of tokens its phrase covers.
///
/// `phrase_len` is `Some` when every remaining query token analyzed to exactly
/// one form, so the whole query maps onto a contiguous run of `merged`
/// positions — the precondition for both the phrase boost and the
/// whole-address match tier (see [`rank_tier`]). It is `None` when a token
/// stemmed or split into several terms (no phrase is built) and for structured
/// requests that carry no free text.
struct BuiltQuery {
    query: Box<dyn Query>,
    phrase_len: Option<usize>,
}

/// AND the segments, and AND the tokens within each. A token is free to match
/// any of the text fields (OR across fields).
///
/// On top of that AND query sits one optional clause: the whole query, in
/// order, as a phrase against `merged` (see [`phrase_query`]). It never
/// filters — a document that matches every token but not the phrase is still a
/// hit — it only lifts documents that read as the address the user typed.
fn assemble(fields: &Fields, segments: &[Segment]) -> Option<BuiltQuery> {
    let target = prefix_target(segments);
    let (last_segment, last_token) = target.unwrap_or((segments.len() - 1, 0));

    let mut segment_queries: Vec<Box<dyn Query>> = Vec::with_capacity(segments.len());
    // One analyzed form per query token, in query order, whenever every token
    // has exactly one — the terms the merged phrase is built from.
    let mut phrase_terms: Vec<String> = Vec::new();
    let mut phrase_possible = true;

    for (si, segment) in segments.iter().enumerate() {
        let mut clauses: Vec<(Occur, Box<dyn Query>)> = Vec::with_capacity(segment.tokens.len());
        for (ti, token) in segment.tokens.iter().enumerate() {
            let prefix = target == Some((si, ti));
            if let Some(clause) =
                token_clause(fields, token, segment.canonical_house.as_deref(), prefix)
            {
                clauses.push((Occur::Must, clause));
            }
            match token.text_forms.as_slice() {
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

    let last_is_house = segments[last_segment].tokens[last_token].is_house;
    let phrase_len = phrase_possible.then_some(phrase_terms.len());
    let phrase_clauses = if phrase_possible {
        phrase_clauses(fields, &phrase_terms, !last_is_house)
    } else {
        Vec::new()
    };

    if phrase_clauses.is_empty() {
        return Some(BuiltQuery {
            query: base,
            phrase_len,
        });
    }

    let mut clauses: Vec<(Occur, Box<dyn Query>)> = Vec::with_capacity(phrase_clauses.len() + 1);
    clauses.push((Occur::Must, base));
    // `Should` beside `Must` is optional: every base match is still a hit,
    // phrase or not. The phrase clauses only raise the score.
    clauses.extend(phrase_clauses);
    Some(BuiltQuery {
        query: Box::new(BooleanQuery::new(clauses)),
        phrase_len,
    })
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
/// like a house number — the house-number fields as well.
///
/// `canonical_house` is the segment-level canonical form (`12 к 1` → `12к1`),
/// offered as one more disjunct so both house-number spellings meet.
///
/// Returns `None` when the token analyzed away to nothing (e.g. it exceeded the
/// length cap), so the caller can drop it instead of adding an empty clause
/// that would match nothing.
fn token_clause(
    fields: &Fields,
    token: &QueryToken,
    canonical_house: Option<&str>,
    prefix: bool,
) -> Option<Box<dyn Query>> {
    let mut clauses: Vec<(Occur, Box<dyn Query>)> =
        Vec::with_capacity(token.text_forms.len() * 8 + 4);

    for form in &token.text_forms {
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

    if token.is_house {
        // Streets carry digits too ("улица 1905 года"), so this is an
        // additional disjunct rather than a replacement. House numbers are
        // never prefix-matched: people type them in full.
        for field in [fields.house_number, fields.house_normalized] {
            let term = Term::from_field_text(field, &token.text);
            clauses.push((
                Occur::Should,
                Box::new(BoostQuery::new(
                    Box::new(TermQuery::new(term.clone(), IndexRecordOption::WithFreqs)),
                    BOOST_HOUSE * EXACT_BOOST,
                )),
            ));
            if token.text.chars().count() >= MIN_PREFIX_LEN {
                clauses.push((
                    Occur::Should,
                    Box::new(BoostQuery::new(
                        Box::new(FuzzyTermQuery::new(term, 1, true)),
                        BOOST_HOUSE * FUZZY_BOOST,
                    )),
                ));
            }
        }
        if let Some(canonical) = canonical_house {
            let term = Term::from_field_text(fields.house_normalized, canonical);
            clauses.push((
                Occur::Should,
                Box::new(BoostQuery::new(
                    Box::new(TermQuery::new(term.clone(), IndexRecordOption::WithFreqs)),
                    BOOST_HOUSE * EXACT_BOOST,
                )),
            ));
            if canonical.chars().count() >= MIN_PREFIX_LEN {
                clauses.push((
                    Occur::Should,
                    Box::new(BoostQuery::new(
                        Box::new(FuzzyTermQuery::new(term, 1, true)),
                        BOOST_HOUSE * FUZZY_BOOST,
                    )),
                ));
            }
        }
    }

    match clauses.len() {
        0 => None,
        1 => clauses.pop().map(|(_, q)| q),
        _ => Some(Box::new(BooleanQuery::new(clauses))),
    }
}

/// Ordering key of a collected hit: match tier, BM25 score, popularity.
///
/// The tier says how much of the document's indexed address (`merged`) the
/// query covers:
///
/// - `3` — the whole address: a k-token phrase fills a `merged` field that is
///   exactly k tokens long and the document has no `region` (the one rendered
///   address part `merged` does not carry). For a single token the one-token
///   address must be the document's `name`. The zone named `Санкт-Петербург`
///   is such a document, which is what puts it ahead of the buildings and
///   streets inside it.
/// - `2` — a strictly contiguous phrase inside a longer address.
/// - `1` — a phrase with at most [`PHRASE_SLOP`] intervening tokens.
/// - `0` — every token matched somewhere, but not as a phrase.
///
/// Tiers 1 and 2 are functions of the score because the phrase tiers are
/// additive constants ([`EXACT_PHRASE_SCORE`], [`PHRASE_SCORE`]) whose
/// magnitude dwarfs any per-token BM25 sum; tier 3 additionally needs the
/// document's `merged` length. Encoding the tier first keeps the "a phrase
/// always wins" guarantee; the ordinary score then keeps street/name/region
/// field boosts meaningful; popularity only breaks the ties BM25 leaves behind.
///
/// Popularity is deliberately *not* ahead of the score: it is a document-level
/// signal, so ranking it first would let a document that matched a cheap field
/// (say a region whose name happens to collide) overtake a real street match
/// just because the document's name is a nationwide chain store.
type RankKey = (u8, Score, u64);

/// Match tier of a collected hit — see [`RankKey`] for what the values mean.
///
/// The token counts come from the fieldnorms
/// ([`SegmentReader::get_fieldnorms_reader`]): exact for lengths up to 40 and
/// quantized above that. A whole-address match longer than 40 tokens could in
/// principle be misread, but no realistic address (and no query under
/// [`MAX_QUERY_LEN`] characters) gets there.
///
/// A k-token phrase can match a k-token `merged` field only by filling it, so
/// for k ≥ 2 the whole-address tier needs the contiguous phrase (the same
/// condition as tier 2) plus the length equality. A single-token query has no
/// phrase clause — the base query already matches the token — so the score
/// cannot say where the token matched. Requiring the one-token address to be
/// the document's `name` excludes the points whose `merged` is just the country
/// (a region-only match, like `Тверская` against `Тверская область`), which
/// would otherwise look like whole matches.
fn rank_tier(score: Score, lens: FieldLens, phrase_len: Option<usize>) -> u8 {
    if let Some(len) = phrase_len {
        let whole_address = lens.region == 0
            && if len == 1 {
                lens.merged == 1 && lens.name == 1
            } else {
                score >= EXACT_PHRASE_SCORE && lens.merged as usize == len
            };
        if whole_address {
            return 3;
        }
    }
    if score >= EXACT_PHRASE_SCORE {
        2
    } else if score >= PHRASE_SCORE {
        1
    } else {
        0
    }
}

/// Token counts of the fields the whole-address tier compares against, read
/// from the segment's fieldnorms.
#[derive(Clone, Copy)]
struct FieldLens {
    /// `merged`: country, city, street, house number and name concatenated —
    /// the field the phrase clauses match.
    merged: u32,
    /// `name`: a zone's entire indexable address, and the only field that can
    /// prove a single-token whole match.
    name: u32,
    /// `region`: the one rendered address part `merged` does not carry. A
    /// whole-address match has no region.
    region: u32,
}

/// Fieldnorm readers for one segment, reused across its documents.
struct WholeAddressNorms {
    merged: Option<FieldNormReader>,
    name: Option<FieldNormReader>,
    region: Option<FieldNormReader>,
}

impl WholeAddressNorms {
    fn new(segment_reader: &SegmentReader, fields: &Fields) -> Self {
        let reader = |field: Field| segment_reader.get_fieldnorms_reader(field).ok();
        Self {
            merged: reader(fields.merged),
            name: reader(fields.name),
            region: reader(fields.region),
        }
    }

    /// A field that records no norms disables the tier rather than misreporting
    /// a length.
    fn lens(&self, doc: DocId) -> FieldLens {
        let len = |reader: &Option<FieldNormReader>| {
            reader
                .as_ref()
                .map_or(u32::MAX, |reader| reader.fieldnorm(doc))
        };
        FieldLens {
            merged: len(&self.merged),
            name: len(&self.name),
            region: len(&self.region),
        }
    }
}

/// Structured address parameters, each matched only against its own index
/// field. Beside the free-text `q` these remove the cross-field noise of a
/// token that matches any field — `city=Moscow` cannot match a street named
/// Moscow.
#[derive(Debug, Clone, Default)]
pub struct StructuredQuery {
    pub city: Option<String>,
    pub region: Option<String>,
    pub street: Option<String>,
    pub house: Option<String>,
    pub name: Option<String>,
}

impl StructuredQuery {
    /// Whether any structured field carries a non-empty value.
    pub fn has_values(&self) -> bool {
        self.provided().next().is_some() || self.house().is_some()
    }

    fn provided(&self) -> impl Iterator<Item = (&str, &str, Score)> {
        [
            (self.region.as_deref(), "region", BOOST_REGION),
            (self.city.as_deref(), "city", BOOST_CITY),
            (self.street.as_deref(), "street", BOOST_STREET),
            (self.name.as_deref(), "name", BOOST_NAME),
        ]
        .into_iter()
        .filter_map(|(value, label, boost)| {
            let value = value.map(str::trim).filter(|v| !v.is_empty())?;
            Some((label, value, boost))
        })
    }

    fn house(&self) -> Option<&str> {
        self.house
            .as_deref()
            .map(str::trim)
            .filter(|v| !v.is_empty())
    }
}

/// Everything a forward geocode request can ask for.
#[derive(Debug, Clone)]
pub struct SearchRequest {
    /// Free-text query (`q`), matched across all fields.
    pub query: Option<String>,
    pub structured: StructuredQuery,
    pub kind: GeocodeKindFilter,
    pub limit: usize,
    pub offset: usize,
    /// Include the full multipolygon for zone hits. Polygon clones are large
    /// for countries, so clients that only need a point can skip them.
    pub include_polygon: bool,
}

impl Default for SearchRequest {
    fn default() -> Self {
        SearchRequest {
            query: None,
            structured: StructuredQuery::default(),
            kind: GeocodeKindFilter::default(),
            limit: DEFAULT_LIMIT,
            offset: 0,
            include_polygon: true,
        }
    }
}

/// AND the free-text query and the structured fields into one query.
///
/// Either side may be absent; `None` means the request carries no constraint at
/// all (an empty `q` and no structured values). The phrase length comes from
/// the free-text half alone — structured values carry no phrase and therefore
/// never trigger the whole-address tier by themselves.
fn build_request_query(
    fields: &Fields,
    analyzers: &Analyzers,
    searcher: &Searcher,
    request: &SearchRequest,
) -> Option<BuiltQuery> {
    let free = request
        .query
        .as_deref()
        .filter(|q| !q.trim().is_empty())
        .and_then(|q| build_query(fields, analyzers, searcher, q));
    let structured = structured_query(fields, analyzers, searcher, &request.structured);

    match (free, structured) {
        (Some(free), Some(structured)) => Some(BuiltQuery {
            query: Box::new(BooleanQuery::new(vec![
                (Occur::Must, free.query),
                (Occur::Must, structured),
            ])),
            phrase_len: free.phrase_len,
        }),
        (Some(query), None) => Some(query),
        (None, Some(query)) => Some(BuiltQuery {
            query,
            phrase_len: None,
        }),
        (None, None) => None,
    }
}

/// One structured field: its tokens are AND-ed, each free to match exactly or
/// (for the last one) as a prefix, with the usual exact/fuzzy pair.
fn field_text_query(
    analyzers: &Analyzers,
    searcher: &Searcher,
    field: Field,
    boost: Score,
    text: &str,
) -> Option<Box<dyn Query>> {
    let mut analyzer = analyzers.text.clone();
    let forms = tokenize(&mut analyzer, text);
    let mut token_clauses: Vec<(Occur, Box<dyn Query>)> = Vec::with_capacity(forms.len());
    let last = forms.len().saturating_sub(1);

    for (i, form) in forms.iter().enumerate() {
        let prefix = i == last;
        // Like the free-text query: an absent token cannot constrain, except
        // the last one, which may still complete as a prefix.
        if !prefix
            && searcher
                .doc_freq(&Term::from_field_text(field, form))
                .unwrap_or(0)
                == 0
        {
            continue;
        }
        let mut alternatives: Vec<(Occur, Box<dyn Query>)> = vec![(
            Occur::Should,
            Box::new(BoostQuery::new(
                exact_query(field, form, prefix),
                boost * EXACT_BOOST,
            )),
        )];
        if let Some(fuzzy) = fuzzy_query(field, form, prefix) {
            alternatives.push((
                Occur::Should,
                Box::new(BoostQuery::new(fuzzy, boost * FUZZY_BOOST)),
            ));
        }
        token_clauses.push((Occur::Must, Box::new(BooleanQuery::new(alternatives))));
    }

    match token_clauses.len() {
        0 => None,
        1 => token_clauses.pop().map(|(_, q)| q),
        _ => Some(Box::new(BooleanQuery::new(token_clauses))),
    }
}

/// Structured house number, matched against the raw and normalized house
/// fields only.
fn field_house_query(fields: &Fields, analyzers: &Analyzers, text: &str) -> Option<Box<dyn Query>> {
    let mut analyzer = analyzers.house.clone();
    let tokens = tokenize(&mut analyzer, text);
    let has_digit = tokens.iter().any(|token| token_has_digit(token));
    let has_alias = tokens.iter().any(|token| is_house_alias(token));
    let canonical = (has_digit && has_alias).then(|| {
        tokens
            .iter()
            .filter(|token| token_has_digit(token) || is_house_alias(token))
            .map(|token| canonical_token(token))
            .collect::<String>()
    });

    let mut token_clauses: Vec<(Occur, Box<dyn Query>)> = Vec::new();
    for token in tokens {
        let is_house = token_has_digit(&token);
        if !is_house {
            continue;
        }
        let mut alternatives: Vec<(Occur, Box<dyn Query>)> = Vec::new();
        for field in [fields.house_number, fields.house_normalized] {
            let term = Term::from_field_text(field, &token);
            alternatives.push((
                Occur::Should,
                Box::new(BoostQuery::new(
                    Box::new(TermQuery::new(term.clone(), IndexRecordOption::WithFreqs)),
                    BOOST_HOUSE * EXACT_BOOST,
                )),
            ));
            if token.chars().count() >= MIN_PREFIX_LEN {
                alternatives.push((
                    Occur::Should,
                    Box::new(BoostQuery::new(
                        Box::new(FuzzyTermQuery::new(term, 1, true)),
                        BOOST_HOUSE * FUZZY_BOOST,
                    )),
                ));
            }
        }
        token_clauses.push((Occur::Must, Box::new(BooleanQuery::new(alternatives))));
    }

    // The canonical form is an *alternative* to the per-token conjunction, not
    // another required clause: `12 к 1` must match a stored `12к1`, which has
    // no separate `12` or `1` tokens.
    let token_conjunction: Option<Box<dyn Query>> = match token_clauses.len() {
        0 => None,
        1 => token_clauses.pop().map(|(_, q)| q),
        _ => Some(Box::new(BooleanQuery::new(token_clauses))),
    };

    let canonical_clause: Option<Box<dyn Query>> = canonical.map(|canonical| {
        let term = Term::from_field_text(fields.house_normalized, &canonical);
        let mut alternatives: Vec<(Occur, Box<dyn Query>)> = vec![(
            Occur::Should,
            Box::new(BoostQuery::new(
                Box::new(TermQuery::new(term.clone(), IndexRecordOption::WithFreqs)),
                BOOST_HOUSE * EXACT_BOOST,
            )),
        )];
        if canonical.chars().count() >= MIN_PREFIX_LEN {
            alternatives.push((
                Occur::Should,
                Box::new(BoostQuery::new(
                    Box::new(FuzzyTermQuery::new(term, 1, true)),
                    BOOST_HOUSE * FUZZY_BOOST,
                )),
            ));
        }
        Box::new(BooleanQuery::new(alternatives)) as Box<dyn Query>
    });

    match (token_conjunction, canonical_clause) {
        (Some(tokens), Some(canonical)) => Some(Box::new(BooleanQuery::new(vec![
            (Occur::Should, tokens),
            (Occur::Should, canonical),
        ]))),
        (Some(query), None) | (None, Some(query)) => Some(query),
        (None, None) => None,
    }
}

/// Build the structured half of a request; `None` when nothing was provided.
fn structured_query(
    fields: &Fields,
    analyzers: &Analyzers,
    searcher: &Searcher,
    structured: &StructuredQuery,
) -> Option<Box<dyn Query>> {
    let mut clauses: Vec<(Occur, Box<dyn Query>)> = Vec::new();

    for (label, value, boost) in structured.provided() {
        let field = match label {
            "region" => fields.region,
            "city" => fields.city,
            "street" => fields.street,
            "name" => fields.name,
            _ => continue,
        };
        if let Some(query) = field_text_query(analyzers, searcher, field, boost, value) {
            clauses.push((Occur::Must, query));
        }
    }

    if let Some(house) = structured.house() {
        if let Some(query) = field_house_query(fields, analyzers, house) {
            clauses.push((Occur::Must, query));
        }
    }

    match clauses.len() {
        0 => None,
        1 => clauses.pop().map(|(_, q)| q),
        _ => Some(Box::new(BooleanQuery::new(clauses))),
    }
}

impl ForwardGeocoder {
    pub fn search(&self, request: &SearchRequest) -> tantivy::Result<Vec<SearchResultItem>> {
        let limit = request.limit.clamp(1, MAX_LIMIT);
        // Pagination is shallow by design: the collector over-fetches a bounded
        // number of documents, so an offset beyond that yields nothing.
        let offset = request.offset.min(MAX_FETCH);

        let searcher = self.index_reader.searcher();
        let Some(BuiltQuery { query, phrase_len }) =
            build_request_query(&self.fields, &self.analyzers, &searcher, request)
        else {
            return Ok(Vec::new());
        };
        let fetch = (limit + offset)
            .saturating_mul(OVERFETCH)
            .clamp(MIN_FETCH, MAX_FETCH);

        // One query carries both tiers: exact/prefix clauses are boosted well
        // above the fuzzy ones, so an exact hit always outranks a near-miss
        // while near-misses still surface when nothing better exists.
        let hits = self.collect(&searcher, query.as_ref(), request.kind, fetch, phrase_len)?;

        let mut results =
            self.collapse(&searcher, hits, limit + offset, request.include_polygon)?;
        if offset > 0 {
            results.drain(..offset.min(results.len()));
        }
        Ok(results)
    }

    /// Distinct term suggestions for the prefix being typed, ranked by
    /// approximate document frequency.
    ///
    /// This reads the term dictionaries directly instead of running a search:
    /// suggestions are distinct strings with counts, not documents, and the
    /// term dictionary is already sorted, so a prefix is a range scan.
    pub fn suggest(&self, input: &str, limit: usize) -> tantivy::Result<Vec<Suggestion>> {
        let searcher = self.index_reader.searcher();
        suggest_terms(&searcher, &self.fields, &self.analyzers, input, limit)
    }

    fn collect(
        &self,
        searcher: &Searcher,
        query: &dyn Query,
        kind: GeocodeKindFilter,
        fetch: usize,
        phrase_len: Option<usize>,
    ) -> tantivy::Result<Vec<(RankKey, DocAddress)>> {
        // `FilterCollector` runs the predicate on the fast-field value, so the
        // kind filter is applied during collection rather than after it.
        //
        // `tweak_score` replaces the raw BM25 score with the rank key
        // `(tier, score, popularity)`: the collector sorts by that key in
        // descending order, so the over-fetch keeps the most relevant members
        // of a score tie instead of an arbitrary subset. The tier needs the
        // token counts of the fields the phrase clauses match and of the parts
        // that decide whether the whole address was matched.
        let fields = self.fields;
        let collector = FilterCollector::new(
            GEO_TYPE_FIELD.to_string(),
            move |value: u64| {
                GeoObjectKind::try_from(value)
                    .map(|obj_type| kind.matches(obj_type))
                    .unwrap_or(false)
            },
            TopDocs::with_limit(fetch).tweak_score(move |segment_reader: &SegmentReader| {
                let popularity = segment_reader
                    .fast_fields()
                    .u64("popularity")
                    .expect("popularity is a fast field in the schema")
                    .first_or_default_col(0u64);
                let norms = WholeAddressNorms::new(segment_reader, &fields);
                move |doc: DocId, score: Score| {
                    (
                        rank_tier(score, norms.lens(doc), phrase_len),
                        score,
                        popularity.get_val(doc),
                    )
                }
            }),
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
        hits: Vec<(RankKey, DocAddress)>,
        keep: usize,
        include_polygon: bool,
    ) -> tantivy::Result<Vec<SearchResultItem>> {
        let mut seen: HashMap<String, usize> = HashMap::new();
        let mut kept: Vec<(RankKey, SearchResultItem)> = Vec::new();

        for (rank, address) in hits {
            let (key, item) = self.materialize(searcher, address, rank.1, include_polygon)?;
            match seen.get(&key) {
                Some(&index) => {
                    if rank > kept[index].0 {
                        kept[index] = (rank, item);
                    }
                }
                None => {
                    seen.insert(key, kept.len());
                    kept.push((rank, item));
                }
            }
        }

        kept.sort_by(|a, b| {
            b.0 .0
                .cmp(&a.0 .0)
                .then_with(|| b.0 .1.total_cmp(&a.0 .1))
                .then_with(|| b.0 .2.cmp(&a.0 .2))
                .then_with(|| kind_rank(b.1.geo_type).cmp(&kind_rank(a.1.geo_type)))
                .then_with(|| a.1.address_string.cmp(&b.1.address_string))
        });
        kept.truncate(keep);

        Ok(kept.into_iter().map(|(_, item)| item).collect())
    }

    /// Read the two fast fields that say what a document points at.
    fn document_location(
        &self,
        searcher: &Searcher,
        address: DocAddress,
    ) -> tantivy::Result<(GeoObjectKind, u64)> {
        let segment_reader = searcher.segment_reader(address.segment_ord);
        let fast_fields = segment_reader.fast_fields();

        let geo_type = fast_fields
            .u64(GEO_TYPE_FIELD)?
            .first(address.doc_id)
            .ok_or_else(|| TantivyError::InvalidArgument("document is missing geo_type".into()))?;
        let cache_location = fast_fields
            .u64("cache_location")?
            .first(address.doc_id)
            .ok_or_else(|| {
                TantivyError::InvalidArgument("document is missing cache_location".into())
            })?;

        Ok((GeoObjectKind::try_from(geo_type)?, cache_location))
    }

    /// Turn a hit into a result, plus the key it collapses under.
    ///
    /// The address parts are resolved from the mmap'd cache rather than from a
    /// stored document: the cache is the single source of truth, and keeping a
    /// second copy of every string in the index only costs space and decode
    /// time.
    fn materialize(
        &self,
        searcher: &Searcher,
        address: DocAddress,
        score: Score,
        include_polygon: bool,
    ) -> tantivy::Result<(String, SearchResultItem)> {
        let (geo_type, cache_location) = self.document_location(searcher, address)?;
        let cache = &self.geocoder.cache;
        let is_zone = geo_type == GeoObjectKind::Zone;

        let (lat, lon, country, parts, multipolygon) = if is_zone {
            let zone = cache.zones.get(cache_location as usize).ok_or_else(|| {
                TantivyError::InvalidArgument(format!(
                    "zone index {cache_location} is out of range for this cache"
                ))
            })?;
            // The centroid is precomputed when the cache is parsed; deriving it
            // here would be O(vertices) on every request.
            (
                zone.centroid.y(),
                zone.centroid.x(),
                String::new(),
                vec![zone.name.clone()],
                include_polygon.then(|| zone.polygon.clone()),
            )
        } else {
            let point = cache.point_at(cache_location as usize);
            let parts = [
                cache.read_string(point.data.region_id.get()),
                cache.read_string(point.data.city_id.get()),
                cache.read_string(point.data.street_id.get()),
                cache.read_string(point.data.house_number_id.get()),
                cache.read_string(point.data.name_id.get()),
            ]
            .into_iter()
            .filter(|part| !part.is_empty())
            .collect();
            let country = self
                .geocoder
                .country_at(point.lon, point.lat)
                .unwrap_or_default()
                .to_string();
            (point.lat, point.lon, country, parts, None)
        };

        let address_string = parts.join(", ");

        let mut key = String::with_capacity(address_string.len() + country.len() + 8);
        key.push_str(&(geo_type as u64).to_string());
        for part in std::iter::once(&country).chain(parts.iter()) {
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
                country,
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
            house_normalized: canonical_house(house_number),
            name: name.to_string(),
            geo_kind: GeoObjectKind::Building,
            popularity: 0,
            cache_location: location,
        }
    }

    impl IndexedDoc {
        fn popular(mut self, popularity: u64) -> Self {
            self.popularity = popularity;
            self
        }
    }

    fn build_test_index(docs: Vec<IndexedDoc>) -> (Index, Fields, Analyzers, Vec<IndexedDoc>) {
        let fixtures = docs.clone();
        let fingerprint = CacheFingerprint {
            format: SIDECAR_FORMAT,
            date_created: "test".to_string(),
            locale: String::new(),
            num_points: docs.len(),
            zones: 0,
            cache_size: 0,
        };
        let (index, fields, analyzers, _mode) =
            build_index(docs.into_iter(), "", None, &fingerprint).unwrap();
        (index, fields, analyzers, fixtures)
    }

    /// Read a u64 fast field for a doc address the way production does.
    fn fast_u64(searcher: &Searcher, field: &str, address: DocAddress) -> u64 {
        searcher
            .segment_reader(address.segment_ord)
            .fast_fields()
            .u64(field)
            .unwrap()
            .first(address.doc_id)
            .unwrap()
    }

    /// Render a fixture the way `materialize` renders the same address parts
    /// (country first, so the fixtures stay distinguishable).
    fn render_fixture(doc: &IndexedDoc) -> String {
        [
            doc.country.as_str(),
            doc.city.as_str(),
            doc.street.as_str(),
            doc.house_number.as_str(),
            doc.name.as_str(),
        ]
        .into_iter()
        .filter(|part| !part.is_empty())
        .collect::<Vec<_>>()
        .join(", ")
    }

    /// Run a built query with the production rank key and render the fixtures.
    fn run_query_with(
        searcher: &Searcher,
        fields: &Fields,
        fixtures: &[IndexedDoc],
        query: &dyn Query,
        phrase_len: Option<usize>,
    ) -> Vec<(RankKey, String)> {
        let fields = *fields;
        let collector =
            TopDocs::with_limit(10).tweak_score(move |segment_reader: &SegmentReader| {
                let popularity = segment_reader
                    .fast_fields()
                    .u64("popularity")
                    .unwrap()
                    .first_or_default_col(0u64);
                let norms = WholeAddressNorms::new(segment_reader, &fields);
                move |doc: DocId, score: Score| {
                    (
                        rank_tier(score, norms.lens(doc), phrase_len),
                        score,
                        popularity.get_val(doc),
                    )
                }
            });
        searcher
            .search(query, &collector)
            .unwrap()
            .into_iter()
            .map(|(rank, address)| {
                let location = fast_u64(searcher, "cache_location", address) as usize;
                (rank, render_fixture(&fixtures[location]))
            })
            .collect()
    }

    /// Run a built query with a fresh searcher.
    fn run_query(
        index: &Index,
        fields: &Fields,
        fixtures: &[IndexedDoc],
        query: &BuiltQuery,
    ) -> Vec<(RankKey, String)> {
        let searcher = index.reader().unwrap().searcher();
        run_query_with(
            &searcher,
            fields,
            fixtures,
            query.query.as_ref(),
            query.phrase_len,
        )
    }

    /// Search and return `(rank key, rendered address)` pairs, in ranked order,
    /// using the same rank key production uses.
    fn search_ranked(
        index: &Index,
        fields: &Fields,
        analyzers: &Analyzers,
        fixtures: &[IndexedDoc],
        query_text: &str,
    ) -> Vec<(RankKey, String)> {
        let searcher = index.reader().unwrap().searcher();
        let query =
            build_query(fields, analyzers, &searcher, query_text).expect("query must build");
        run_query(index, fields, fixtures, &query)
    }

    /// Search with a full request (structured fields, offset, …).
    fn search_request(
        index: &Index,
        fields: &Fields,
        analyzers: &Analyzers,
        fixtures: &[IndexedDoc],
        request: &SearchRequest,
    ) -> Vec<(RankKey, String)> {
        let searcher = index.reader().unwrap().searcher();
        let query = build_request_query(fields, analyzers, &searcher, request)
            .expect("request must build a query");
        run_query(index, fields, fixtures, &query)
    }

    /// Search and render the fixture addresses, in ranked order.
    fn search(
        index: &Index,
        fields: &Fields,
        analyzers: &Analyzers,
        fixtures: &[IndexedDoc],
        query_text: &str,
    ) -> Vec<String> {
        search_ranked(index, fields, analyzers, fixtures, query_text)
            .into_iter()
            .map(|(_, address)| address)
            .collect()
    }

    /// Search and return `(score, rendered address)` pairs, in ranked order.
    fn search_scored(
        index: &Index,
        fields: &Fields,
        analyzers: &Analyzers,
        fixtures: &[IndexedDoc],
        query_text: &str,
    ) -> Vec<(Score, String)> {
        search_ranked(index, fields, analyzers, fixtures, query_text)
            .into_iter()
            .map(|(rank, address)| (rank.1, address))
            .collect()
    }

    #[test]
    fn merged_address_skips_empty_parts() {
        let doc = indexed_doc("Russia", "", "High Street", "12", "", 0);
        assert_eq!(doc.merged_address(), "Russia, High Street, 12");
    }

    #[test]
    fn phrase_outranks_the_same_tokens_in_another_order() {
        let fixtures = vec![
            indexed_doc("Russia", "London", "High Street", "12", "", 0),
            indexed_doc("Russia", "London", "", "12", "High Street", 1),
        ];
        let (index, fields, analyzers, fixtures) = build_test_index(fixtures);

        let results = search(
            &index,
            &fields,
            &analyzers,
            &fixtures,
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
        let (index, fields, analyzers, fixtures) = build_test_index(vec![one_gap, contiguous]);

        let results = search_scored(
            &index,
            &fields,
            &analyzers,
            &fixtures,
            "russia london high 12",
        );

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
        let (index, fields, analyzers, fixtures) = build_test_index(vec![indexed_doc(
            "Russia",
            "London",
            "High Street",
            "",
            "",
            0,
        )]);

        let results = search(
            &index,
            &fields,
            &analyzers,
            &fixtures,
            "russia london high stre",
        );

        assert_eq!(results, vec!["Russia, London, High Street".to_string()]);
    }

    #[test]
    fn country_field_is_searchable() {
        let (index, fields, analyzers, fixtures) = build_test_index(vec![
            indexed_doc("Russia", "Moscow", "Tverskaya", "12", "", 0),
            indexed_doc("", "Moscow", "Tverskaya", "12", "", 1),
        ]);

        let results = search(&index, &fields, &analyzers, &fixtures, "russia");

        assert_eq!(results, vec!["Russia, Moscow, Tverskaya, 12".to_string()]);
    }

    #[test]
    fn popularity_breaks_score_ties() {
        // Both streets match "high" identically (same term, same field, same
        // length), so only the popularity signal can order them.
        let (index, fields, analyzers, fixtures) = build_test_index(vec![
            indexed_doc("", "London", "High Street", "", "", 0).popular(100),
            indexed_doc("", "London", "High Road", "", "", 1).popular(1),
        ]);

        let results = search(&index, &fields, &analyzers, &fixtures, "high");

        assert_eq!(
            results,
            vec![
                "London, High Street".to_string(),
                "London, High Road".to_string()
            ],
            "the more popular street must come first"
        );
    }

    #[test]
    fn phrase_tier_outranks_popularity() {
        // The contiguous phrase sits below the exact-phrase constants and must
        // win despite the other document's popularity.
        let contiguous = indexed_doc("", "London", "High", "12", "Street", 0).popular(0);
        let popular_gap = indexed_doc("", "London", "High Street", "12", "", 1).popular(1_000_000);
        let (index, fields, analyzers, fixtures) = build_test_index(vec![contiguous, popular_gap]);

        let results = search(&index, &fields, &analyzers, &fixtures, "london high 12");

        assert_eq!(
            results.first().map(String::as_str),
            Some("London, High, 12, Street"),
            "the phrase tier must outrank popularity"
        );
    }

    #[test]
    fn whole_address_match_outranks_longer_phrase_hits() {
        // The zone's whole address is the query; the building contains the same
        // two tokens as a contiguous phrase inside a longer address. Both reach
        // the exact-phrase tier, and the building's BM25 is higher (it matches
        // its city field too) — only the whole-address tier can order them.
        let mut zone = indexed_doc("", "", "", "", "New York", 0);
        zone.geo_kind = GeoObjectKind::Zone;
        let building = indexed_doc("United States", "New York", "Broadway", "1", "", 1);
        let (index, fields, analyzers, fixtures) = build_test_index(vec![zone, building]);

        let results = search_ranked(&index, &fields, &analyzers, &fixtures, "new york");

        assert_eq!(results.len(), 2);
        assert_eq!(
            results[0].0 .0, 3,
            "the whole-address match must take the top tier"
        );
        assert_eq!(results[0].1, "New York");
        assert_eq!(
            (results[1].0 .0, results[1].1.as_str()),
            (2, "United States, New York, Broadway, 1"),
            "a phrase hit inside a longer address stays in tier 2"
        );
    }

    #[test]
    fn single_token_whole_address_match_wins() {
        // A one-token query builds no phrase clause at all — the base query
        // already matches the token — so the whole-address tier comes from the
        // document's one-token address alone.
        let mut zone = indexed_doc("", "", "", "", "Moscow", 0);
        zone.geo_kind = GeoObjectKind::Zone;
        let building = indexed_doc("Testland", "Moscow", "High Street", "12", "", 1);
        let (index, fields, analyzers, fixtures) = build_test_index(vec![zone, building]);

        let results = search_ranked(&index, &fields, &analyzers, &fixtures, "moscow");

        assert_eq!(
            (results[0].0 .0, results[0].1.as_str()),
            (3, "Moscow"),
            "a one-token address that is the query must outrank a longer one"
        );
        assert_eq!(results[1].0 .0, 0, "the longer address has no phrase tier");
    }

    #[test]
    fn region_only_match_is_not_a_whole_address_match() {
        // The first document's `merged` is just the country, so it is one token
        // long — but the query only reached it through the region. Treating
        // that as a whole-address match would bury every real address hit.
        let mut region_only = indexed_doc("Testland", "", "", "", "", 0);
        region_only.region = "High Street".to_string();
        let street = indexed_doc("", "", "High Street", "", "", 1);
        let (index, fields, analyzers, fixtures) = build_test_index(vec![region_only, street]);

        let results = search_ranked(&index, &fields, &analyzers, &fixtures, "high");

        assert_eq!(results.len(), 2);
        assert!(
            results.iter().all(|(rank, _)| rank.0 < 3),
            "a region-only match must never reach the whole-address tier"
        );
        assert_eq!(
            results.first().map(|(_, address)| address.as_str()),
            Some("High Street"),
            "the document whose address carries the token must win"
        );
    }

    #[test]
    fn whole_address_match_wins_while_the_last_token_is_typed() {
        let mut zone = indexed_doc("", "", "", "", "New York", 0);
        zone.geo_kind = GeoObjectKind::Zone;
        let building = indexed_doc("United States", "New York", "Broadway", "1", "", 1);
        let (index, fields, analyzers, fixtures) = build_test_index(vec![zone, building]);

        let results = search_ranked(&index, &fields, &analyzers, &fixtures, "new yor");

        assert_eq!(
            (results[0].0 .0, results[0].1.as_str()),
            (3, "New York"),
            "the prefix phrase must still reach the whole-address tier"
        );
    }

    #[test]
    fn absent_non_final_token_is_dropped() {
        let (index, fields, analyzers, fixtures) =
            build_test_index(vec![indexed_doc("", "London", "High Street", "12", "", 0)]);

        // "city" exists in no document and is not the token being typed, so it
        // must not make the query unsatisfiable.
        let results = search(&index, &fields, &analyzers, &fixtures, "city london");

        assert_eq!(results, vec!["London, High Street, 12".to_string()]);
    }

    #[test]
    fn house_aliases_and_compact_forms_meet() {
        let (index, fields, analyzers, fixtures) = build_test_index(vec![
            indexed_doc("", "Moscow", "Tverskaya", "12 к 1", "", 0),
            indexed_doc("", "Moscow", "Tverskaya", "12к1", "", 1),
        ]);

        // The alias spelling finds the compact one through house_normalized…
        let spacy = search(&index, &fields, &analyzers, &fixtures, "12 к 1");
        assert_eq!(spacy.len(), 2, "the alias spelling must find both forms");

        // …and the compact spelling finds the alias one.
        let compact = search(&index, &fields, &analyzers, &fixtures, "12к1");
        assert_eq!(
            compact.len(),
            2,
            "the compact spelling must find both forms"
        );
    }

    #[test]
    fn structured_city_does_not_match_a_street() {
        let (index, fields, analyzers, fixtures) = build_test_index(vec![
            indexed_doc("", "Moscow", "Tverskaya", "", "", 0),
            indexed_doc("", "London", "Moscow Street", "", "", 1),
        ]);
        let request = SearchRequest {
            structured: StructuredQuery {
                city: Some("Moscow".to_string()),
                ..Default::default()
            },
            ..Default::default()
        };

        let results = search_request(&index, &fields, &analyzers, &fixtures, &request);

        let addresses: Vec<String> = results.into_iter().map(|(_, address)| address).collect();
        assert_eq!(
            addresses,
            vec!["Moscow, Tverskaya".to_string()],
            "a city parameter must not match a street of the same name"
        );
    }

    #[test]
    fn structured_fields_are_anded_with_free_text() {
        let (index, fields, analyzers, fixtures) = build_test_index(vec![
            indexed_doc("", "Moscow", "Tverskaya", "", "", 0),
            indexed_doc("", "Moscow", "Arbat", "", "", 1),
            indexed_doc("", "London", "Tverskaya", "", "", 2),
        ]);
        let request = SearchRequest {
            query: Some("moscow".to_string()),
            structured: StructuredQuery {
                street: Some("Tverskaya".to_string()),
                ..Default::default()
            },
            ..Default::default()
        };

        let results = search_request(&index, &fields, &analyzers, &fixtures, &request);

        let addresses: Vec<String> = results.into_iter().map(|(_, address)| address).collect();
        assert_eq!(addresses, vec!["Moscow, Tverskaya".to_string()]);
    }

    #[test]
    fn structured_house_matches_both_spellings() {
        let (index, fields, analyzers, fixtures) = build_test_index(vec![
            indexed_doc("", "Moscow", "Tverskaya", "12 к 1", "", 0),
            indexed_doc("", "Moscow", "Tverskaya", "12к1", "", 1),
        ]);
        let request = SearchRequest {
            structured: StructuredQuery {
                house: Some("12 к 1".to_string()),
                ..Default::default()
            },
            ..Default::default()
        };

        let results = search_request(&index, &fields, &analyzers, &fixtures, &request);

        assert_eq!(results.len(), 2, "both house spellings must match");
    }

    #[test]
    fn suggestions_come_from_the_term_dictionary() {
        let (index, fields, analyzers, _fixtures) = build_test_index(vec![
            indexed_doc("", "Moscow", "", "", "", 0),
            indexed_doc("", "Moscow", "", "", "", 1),
            indexed_doc("", "Moscow", "", "", "", 2),
            indexed_doc("", "Mombasa", "", "", "", 3),
        ]);
        let searcher = index.reader().unwrap().searcher();

        let suggestions = suggest_terms(&searcher, &fields, &analyzers, "mo", 10).unwrap();

        let moscow = suggestions
            .iter()
            .find(|s| s.text == "moscow")
            .expect("moscow must be suggested");
        assert_eq!(moscow.doc_freq, 3);
        let mombasa = suggestions
            .iter()
            .find(|s| s.text == "mombasa")
            .expect("mombasa must be suggested");
        assert_eq!(mombasa.doc_freq, 1);
        assert_eq!(
            suggestions[0].text, "moscow",
            "the more frequent term must rank first"
        );
    }

    #[test]
    fn prefix_upper_bound_increments_the_last_character() {
        assert_eq!(prefix_upper_bound("mo"), "mp");
        assert_eq!(prefix_upper_bound("m"), "n");
        assert_eq!(prefix_upper_bound(""), "");
    }

    /// Benchmark-style harness for tuning the query constants; not run by
    /// default.
    ///
    /// `cargo test --release --manifest-path server_rs/Cargo.toml -- --ignored
    /// --nocapture bench_forward`
    #[test]
    #[ignore = "manual benchmark"]
    fn bench_forward() {
        let mut docs = Vec::with_capacity(50_000);
        let cities = ["Moscow", "London", "Paris", "Berlin", "Madrid"];
        let streets = [
            "High Street",
            "Main Road",
            "Station Road",
            "Church Lane",
            "Park Avenue",
        ];
        for i in 0..50_000u64 {
            let city = cities[(i as usize) % cities.len()];
            let street = streets[((i / cities.len() as u64) as usize) % streets.len()];
            docs.push(
                indexed_doc("Testland", city, street, &(i % 200).to_string(), "", i).popular(i),
            );
        }
        let (index, fields, analyzers, fixtures) = build_test_index(docs);
        let searcher = index.reader().unwrap().searcher();

        let iterations = 200;
        for query in ["high", "high st", "moscow high", "station roa", "12"] {
            let Some(warmup) = build_query(&fields, &analyzers, &searcher, query) else {
                continue;
            };
            let _ = run_query_with(
                &searcher,
                &fields,
                &fixtures,
                warmup.query.as_ref(),
                warmup.phrase_len,
            );
            let started = std::time::Instant::now();
            for _ in 0..iterations {
                let Some(q) = build_query(&fields, &analyzers, &searcher, query) else {
                    continue;
                };
                let _ = run_query_with(
                    &searcher,
                    &fields,
                    &fixtures,
                    q.query.as_ref(),
                    q.phrase_len,
                );
            }
            println!(
                "{query:?}: {:.3} ms/query over {} docs",
                started.elapsed().as_secs_f64() * 1000.0 / f64::from(iterations),
                fixtures.len()
            );
        }
    }

    #[test]
    fn geo_kind_comes_from_the_cache_geo_type() {
        // Explicit cache types win over what the weight alone would imply.
        assert_eq!(GeoObjectKind::from_cache(1, 5), GeoObjectKind::Building);
        assert_eq!(GeoObjectKind::from_cache(2, 10), GeoObjectKind::Road);
        assert_eq!(GeoObjectKind::from_cache(3, 3), GeoObjectKind::Area);
    }

    #[test]
    fn geo_kind_falls_back_to_weight_for_legacy_records() {
        // geo_type == 0 (legacy 21-byte record) falls back to the weight proxy.
        assert_eq!(GeoObjectKind::from_cache(0, 5), GeoObjectKind::Road);
        assert_eq!(GeoObjectKind::from_cache(0, 10), GeoObjectKind::Building);
        assert_eq!(GeoObjectKind::from_cache(0, 3), GeoObjectKind::Area);
        assert_eq!(GeoObjectKind::from_cache(0, 2), GeoObjectKind::Area);
        // Unrecognised future values also fall back rather than fail.
        assert_eq!(GeoObjectKind::from_cache(99, 5), GeoObjectKind::Road);
    }
}
