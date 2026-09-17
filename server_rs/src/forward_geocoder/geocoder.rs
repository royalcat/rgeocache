use std::collections::HashMap;
use std::str::FromStr;
use std::sync::Arc;

use geo::Centroid;
use strum::EnumString;
use tantivy::collector::{FilterCollector, TopDocs};
use tantivy::query::{BooleanQuery, BoostQuery, FuzzyTermQuery, Occur, Query, TermQuery};
use tantivy::schema::*;
use tantivy::space_usage::PerFieldSpaceUsage;
use tantivy::{
    doc, DocAddress, Index, IndexReader, IndexWriter, Score, Searcher, TantivyDocument,
    TantivyError, Term,
};
use tantivy::{tokenizer::*, ByteCount};

use super::text_analyzer::{build_analyzers, Analyzers};
use crate::cache::{CacheFile, IndexedZone};

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
fn kind_rank(obj_type: GeoObjectType) -> u8 {
    match obj_type {
        GeoObjectType::Building => 2,
        GeoObjectType::Road => 1,
        GeoObjectType::Zone => 0,
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
pub enum GeoObjectType {
    #[strum(serialize = "zone", serialize = "z")]
    Zone = 1,
    #[strum(serialize = "building", serialize = "b")]
    Building = 2,
    #[strum(serialize = "road", serialize = "r")]
    Road = 3,
}

impl GeoObjectType {
    /// Derive the object kind from the cache `weight` byte.
    ///
    /// TODO: replace with an explicit object-type field once the cache format
    /// carries one. `weight` is a lossy proxy — it also encodes the area
    /// sub-kind (3 = industrial, 2 = protected), and those surface as
    /// `Building` for now.
    pub fn from_weight(weight: u8) -> GeoObjectType {
        match weight {
            5 => GeoObjectType::Road,
            _ => GeoObjectType::Building,
        }
    }
}

impl TryFrom<u64> for GeoObjectType {
    type Error = TantivyError;

    fn try_from(value: u64) -> Result<Self, Self::Error> {
        match value {
            1 => Ok(GeoObjectType::Zone),
            2 => Ok(GeoObjectType::Building),
            3 => Ok(GeoObjectType::Road),
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
            match GeoObjectType::from_str(part) {
                Ok(GeoObjectType::Zone) => filter.zones = true,
                Ok(GeoObjectType::Building) => filter.buildings = true,
                Ok(GeoObjectType::Road) => filter.roads = true,
                Err(_) => log::warn!("ignoring unknown kind filter value: {part:?}"),
            }
        }
        filter
    }
}

impl GeocodeKindFilter {
    fn matches(&self, obj_type: GeoObjectType) -> bool {
        match obj_type {
            GeoObjectType::Zone => self.zones,
            GeoObjectType::Building => self.buildings,
            GeoObjectType::Road => self.roads,
        }
    }
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy)]
struct Fields {
    region: Field,
    city: Field,
    street: Field,
    house_number: Field,
    name: Field,
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

    let fields = Fields {
        region: schema_builder.add_text_field("region", text_options(TEXT_ANALYZER)),
        city: schema_builder.add_text_field("city", text_options(TEXT_ANALYZER)),
        street: schema_builder.add_text_field("street", text_options(TEXT_ANALYZER)),
        house_number: schema_builder.add_text_field("house_number", text_options(HOUSE_ANALYZER)),
        name: schema_builder.add_text_field("name", text_options(TEXT_ANALYZER)),
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
    region: String,
    city: String,
    street: String,
    house_number: String,
    name: String,
    geo_type: GeoObjectType,
    /// Where this document's geometry lives in the cache; `geo_type` says how
    /// to read it:
    ///
    /// - point documents: the sorted KD-tree position, resolved through
    ///   [`PointCoords`] (which is `CacheFile::read_coord` in production);
    /// - zone documents: an index into `CacheFile::zones`.
    ///
    /// Coordinates are deliberately *not* stored: they are high-entropy and
    /// compress to nothing in the doc store, while the cache already holds them.
    cache_location: u64,
}

fn build_index(
    docs: impl Iterator<Item = IndexedDoc>,
    locale: &str,
) -> tantivy::Result<(IndexReader, Fields, Analyzers)> {
    let (schema, fields) = build_schema();
    let index = Index::create_from_tempdir(schema)?;

    let analyzers = build_analyzers(locale);
    log::info!(
        "forward geocoder: locale={locale:?} stemmer_language={:?}",
        analyzers.stemmer_language()
    );
    index
        .tokenizers()
        .register(TEXT_ANALYZER, analyzers.text.clone());
    index
        .tokenizers()
        .register(HOUSE_ANALYZER, analyzers.house.clone());

    let mut index_writer: IndexWriter = index.writer(256 * 1024 * 1024)?;

    for d in docs {
        let document = doc!(
            fields.region => d.region,
            fields.city => d.city,
            fields.street => d.street,
            fields.house_number => d.house_number,
            fields.name => d.name,
            fields.geo_type => d.geo_type as u64,
            fields.cache_location => d.cache_location,
        );
        index_writer.add_document(document)?;
    }

    index_writer.commit()?;
    index_writer.wait_merging_threads()?;

    let reader = index.reader()?;

    print_usage(&reader);

    Ok((reader, fields, analyzers))
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
    reader: IndexReader,
    fields: Fields,
    analyzers: Analyzers,
    zones: Arc<[IndexedZone]>,
    cache: Arc<CacheFile>,
}

impl ForwardGeocoder {
    pub fn build(cache: Arc<CacheFile>) -> tantivy::Result<ForwardGeocoder> {
        let points = cache.iter_points().map(|p| IndexedDoc {
            region: cache.read_string(p.data.region_id.get()),
            city: cache.read_string(p.data.city_id.get()),
            street: cache.read_string(p.data.street_id.get()),
            house_number: cache.read_string(p.data.house_number_id.get()),
            name: cache.read_string(p.data.name_id.get()),
            geo_type: GeoObjectType::from_weight(p.data.weight),
            cache_location: p.location,
        });

        // Zones are few relative to points; materialising them keeps the borrow of
        // `skipped` simple and lets us chain two different iterator types.
        let mut skipped_zones = 0usize;
        let mut zone_docs = Vec::with_capacity(cache.zones.len());
        for (i, z) in cache.zones.iter().enumerate() {
            // A zone with no centroid cannot be a search result, and degenerate
            // polygons do occur — skip rather than panic the build thread. `i` must
            // keep addressing `cache.zones`, so use the enumerate index, not the
            // position in `zone_docs`.
            if z.polygon.centroid().is_none() {
                skipped_zones += 1;
                continue;
            }
            // The centroid itself is recomputed from the polygon at query time
            // (see `materialize`) rather than stored.
            zone_docs.push(IndexedDoc {
                region: String::new(),
                city: String::new(),
                street: String::new(),
                house_number: String::new(),
                name: z.name.clone(),
                geo_type: GeoObjectType::Zone,
                cache_location: i as u64,
            });
        }
        if skipped_zones > 0 {
            log::warn!("forward geocoder: skipped {skipped_zones} zones without a centroid");
        }

        let (reader, fields, analyzers) = build_index(points.chain(zone_docs), &cache.locale)?;

        Ok(ForwardGeocoder {
            reader,
            fields,
            analyzers,
            zones: cache.zones.clone(),
            cache: cache.clone(),
        })
    }
}

pub struct SearchResultItem {
    pub address_string: String,
    pub score: Score,
    pub point: (f64, f64),
    pub geo_type: GeoObjectType,
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

impl ForwardGeocoder {
    pub fn search(
        &self,
        query_text: &str,
        kind: GeocodeKindFilter,
        limit: usize,
    ) -> tantivy::Result<Vec<SearchResultItem>> {
        let limit = limit.clamp(1, MAX_LIMIT);

        let Some(query) = self.build_query(query_text) else {
            return Ok(Vec::new());
        };

        let searcher = self.reader.searcher();
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
                GeoObjectType::try_from(value)
                    .map(|obj_type| kind.matches(obj_type))
                    .unwrap_or(false)
            },
            TopDocs::with_limit(fetch).order_by_score(),
        );
        searcher.search(query, &collector)
    }

    /// Build a query for raw user input.
    ///
    /// The input is tokenized with the same analyzers used at index time and
    /// turned into `TermQuery`s programmatically. Nothing from the request ever
    /// reaches tantivy's query grammar, so `foo:bar`, `[a TO b]`, `-term` and
    /// friends are literal text rather than syntax — and can never fail to parse.
    fn build_query(&self, raw: &str) -> Option<Box<dyn Query>> {
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

        let mut text_analyzer = self.analyzers.text.clone();
        let mut house_analyzer = self.analyzers.house.clone();

        let mut segment_tokens: Vec<Vec<(bool, String)>> = Vec::with_capacity(segments.len());
        for segment in &segments {
            let tokens: Vec<(bool, String)> = tokenize(&mut house_analyzer, segment)
                .into_iter()
                .map(|token| {
                    let is_house = token.chars().any(|c| c.is_ascii_digit());
                    (is_house, token)
                })
                // Short type words carry no signal and are in no document, so
                // they can only make the query unsatisfiable. Anything with a
                // digit survives regardless of length (house numbers).
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

        self.assemble(&segment_tokens, &mut text_analyzer)
    }

    /// AND the segments, and AND the tokens within each. A token is free to
    /// match any of the text fields (OR across fields).
    fn assemble(
        &self,
        segment_tokens: &[Vec<(bool, String)>],
        text_analyzer: &mut TextAnalyzer,
    ) -> Option<Box<dyn Query>> {
        let last_segment = segment_tokens.len() - 1;
        let last_token = segment_tokens[last_segment].len() - 1;

        let mut segment_queries: Vec<Box<dyn Query>> = Vec::with_capacity(segment_tokens.len());
        for (si, tokens) in segment_tokens.iter().enumerate() {
            let mut clauses: Vec<(Occur, Box<dyn Query>)> = Vec::with_capacity(tokens.len());
            for (ti, (is_house, token)) in tokens.iter().enumerate() {
                // The house analyzer gives us the canonical (lowercased,
                // unstemmed) form; re-analyzing that single token with the text
                // analyzer yields the term actually present in the index.
                let text_forms = tokenize(text_analyzer, token);
                // Only the very last token of the query completes as a prefix —
                // that is the one the user is still typing.
                let prefix = si == last_segment && ti == last_token && !*is_house;
                if let Some(clause) = self.token_clause(token, &text_forms, *is_house, prefix) {
                    clauses.push((Occur::Must, clause));
                }
            }
            if clauses.is_empty() {
                return None;
            }
            segment_queries.push(Box::new(BooleanQuery::new(clauses)));
        }

        match segment_queries.as_slice() {
            [only] => Some(only.box_clone()),
            _ => Some(Box::new(BooleanQuery::new(
                segment_queries
                    .iter()
                    .map(|q| (Occur::Must, q.box_clone()))
                    .collect(),
            ))),
        }
    }

    /// One query token: it may match any of the text fields, and — when it
    /// looks like a house number — the house-number field as well.
    ///
    /// Returns `None` when the token analyzed away to nothing (e.g. it exceeded
    /// the length cap), so the caller can drop it instead of adding an empty
    /// clause that would match nothing.
    fn token_clause(
        &self,
        raw_token: &str,
        text_forms: &[String],
        is_house: bool,
        prefix: bool,
    ) -> Option<Box<dyn Query>> {
        let mut clauses: Vec<(Occur, Box<dyn Query>)> =
            Vec::with_capacity(text_forms.len() * 8 + 2);

        for form in text_forms {
            for (field, field_boost) in [
                (self.fields.street, BOOST_STREET),
                (self.fields.name, BOOST_NAME),
                (self.fields.city, BOOST_CITY),
                (self.fields.region, BOOST_REGION),
            ] {
                // Both tiers are offered for every token, so a token relaxes
                // independently: one that matches exactly still outranks a
                // sibling that only matched fuzzily. The gap between the two
                // boosts is what keeps an exact hit ahead of a near-miss even
                // though BM25 scores them off different idfs.
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
            let term = Term::from_field_text(self.fields.house_number, raw_token);
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

        let geo_type = GeoObjectType::try_from(
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
        let is_zone = geo_type == GeoObjectType::Zone;

        let (lat, lon, multipolygon) = if is_zone {
            let zone = self.zones.get(cache_location as usize).ok_or_else(|| {
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
