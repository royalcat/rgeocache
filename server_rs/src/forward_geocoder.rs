use std::collections::HashSet;
use std::sync::Arc;

use geo::Centroid;
use strum::EnumString;
use tantivy::collector::{FilterCollector, TopDocs};
use tantivy::query::QueryParser;
use tantivy::schema::*;
use tantivy::{doc, Index, IndexWriter};

use crate::cache::{self, CacheFile};

use tantivy::tokenizer::*;

#[derive(Debug, Clone)]
pub struct ForwardGeocoder {
    cache: Arc<CacheFile>,
    index: Index,
    geo_type_field: Field,
    region_field: Field,
    city_field: Field,
    street_field: Field,
    house_number_field: Field,
    name_field: Field,
    lat_field: Field,
    lon_field: Field,
    i_field: Field,
}

/// Token filter that replaces hyphens with spaces.
// #[derive(Clone)]
// pub struct HipenRemover;

// impl TokenFilter for HipenRemover {
//     type Tokenizer<T: Tokenizer> = HipenRemoverFilter<T>;

//     fn transform<T: Tokenizer>(self, tokenizer: T) -> Self::Tokenizer<T> {
//         HipenRemoverFilter {
//             tokenizer,
//             buffer: String::new(),
//         }
//     }
// }

// #[derive(Clone)]
// pub struct HipenRemoverFilter<T> {
//     tokenizer: T,
//     buffer: String,
// }

// impl<T: Tokenizer> Tokenizer for HipenRemoverFilter<T> {
//     type TokenStream<'a> = HipenRemoverTokenStream<'a, T::TokenStream<'a>>;

//     fn token_stream<'a>(&'a mut self, text: &'a str) -> Self::TokenStream<'a> {
//         self.buffer.clear();
//         HipenRemoverTokenStream {
//             tail: self.tokenizer.token_stream(text),
//             buffer: &mut self.buffer,
//         }
//     }
// }

// pub struct HipenRemoverTokenStream<'a, T> {
//     buffer: &'a mut String,
//     tail: T,
// }

// impl<T: TokenStream> TokenStream for HipenRemoverTokenStream<'_, T> {
//     fn advance(&mut self) -> bool {
//         if !self.tail.advance() {
//             return false;
//         }
//         if self.token_mut().text.is_ascii() {
//             // fast track for ascii.
//             self.token_mut().text.make_ascii_lowercase();
//         } else {
//             to_lowercase_unicode(&self.tail.token().text, self.buffer);
//             mem::swap(&mut self.tail.token_mut().text, self.buffer);
//         }
//         true
//     }

//     fn token(&self) -> &Token {
//         self.tail.token()
//     }

//     fn token_mut(&mut self) -> &mut Token {
//         self.tail.token_mut()
//     }
// }
//

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, EnumString, strum::Display)]
pub enum GeoObjectType {
    #[strum(serialize = "zone", serialize = "z")]
    Zone = 1,
    #[strum(serialize = "building", serialize = "b")]
    Building = 2,
    #[strum(serialize = "road", serialize = "r")]
    Road = 3,
}

impl Into<u64> for GeoObjectType {
    fn into(self) -> u64 {
        self as u64
    }
}

impl TryFrom<u64> for GeoObjectType {
    type Error = &'static str;

    fn try_from(value: u64) -> Result<Self, Self::Error> {
        match value {
            1 => Ok(GeoObjectType::Zone),
            2 => Ok(GeoObjectType::Building),
            3 => Ok(GeoObjectType::Road),
            _ => Err("invalid geo object type"),
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub struct GeocodeTypeFilter {
    zones: bool,
    buildings: bool,
    roads: bool,
}

impl From<String> for GeocodeTypeFilter {
    fn from(s: String) -> Self {
        s.as_str().into()
    }
}

impl From<&str> for GeocodeTypeFilter {
    fn from(s: &str) -> Self {
        let parts = s.split(',').collect::<Vec<&str>>();
        GeocodeTypeFilter {
            zones: parts.contains(&"zone"),
            buildings: parts.contains(&"building"),
            roads: parts.contains(&"road"),
        }
    }
}

impl Default for GeocodeTypeFilter {
    fn default() -> Self {
        GeocodeTypeFilter {
            zones: true,
            buildings: true,
            roads: true,
        }
    }
}

impl GeocodeTypeFilter {
    fn matches<T>(&self, obj_type: T) -> bool
    where
        T: Into<GeoObjectType>,
    {
        match obj_type.into() {
            GeoObjectType::Zone => self.zones,
            GeoObjectType::Building => self.buildings,
            GeoObjectType::Road => self.roads,
        }
    }
}

pub fn build_geocoder(cache: Arc<CacheFile>) -> tantivy::Result<ForwardGeocoder> {
    let mut schema_builder = Schema::builder();

    // TODO custom tokinizer for geo nedded
    let ru_geo_tokinizer_name = "ru_geo";
    let ru_geo_tokinizer = TextAnalyzer::builder(SimpleTokenizer::default())
        .filter(LowerCaser)
        .filter(Stemmer::new(Language::Russian))
        .build();
    // let ru_geo_tokinizer = NgramTokenizer::new(2, 3, false)?;
    let address_part_option = TextOptions::default()
        .set_indexing_options(
            TextFieldIndexing::default()
                .set_tokenizer(ru_geo_tokinizer_name)
                .set_index_option(IndexRecordOption::WithFreqs),
        )
        .set_stored();

    let region_field = schema_builder.add_text_field("region", address_part_option.clone());
    let city_field = schema_builder.add_text_field("city", address_part_option.clone());
    let street_field = schema_builder.add_text_field("street", address_part_option.clone());
    let house_number_field =
        schema_builder.add_text_field("house_number", address_part_option.clone());
    let name_field = schema_builder.add_text_field("name", address_part_option.clone());
    let geo_type_field = schema_builder.add_u64_field("geo_type", FAST | STORED);
    let lat_field = schema_builder.add_f64_field("lat", STORED);
    let lon_field = schema_builder.add_f64_field("lon", STORED);
    let i_field = schema_builder.add_u64_field("i", STORED);
    let schema = schema_builder.build();

    let index = Index::create_from_tempdir(schema)?;

    index
        .tokenizers()
        .register(ru_geo_tokinizer_name, ru_geo_tokinizer);

    let mut index_writer: IndexWriter = index.writer(256 * 1024 * 1024)?;

    for p in cache.iter_points() {
        index_writer.add_document(doc!(
            region_field => cache.read_string(p.data.region_id.get()),
            city_field => cache.read_string(p.data.city_id.get()),
            street_field => cache.read_string(p.data.street_id.get()),
            house_number_field => cache.read_string(p.data.house_number_id.get()),
            name_field => cache.read_string(p.data.name_id.get()),
            geo_type_field => GeoObjectType::Building as u64,
            lat_field => p.lat,
            lon_field => p.lon
        ))?;
    }

    for (i, z) in cache.zones.iter().enumerate() {
        let centroid = z.polygon.centroid().unwrap();

        index_writer.add_document(doc!(
            name_field => z.name.as_str(),
            geo_type_field => GeoObjectType::Zone as u64,
            lat_field => centroid.y(),
            lon_field => centroid.x(),
            i_field => i as u64,
        ))?;
    }

    index_writer.commit()?;

    Ok(ForwardGeocoder {
        index,
        cache,
        geo_type_field,
        region_field,
        city_field,
        street_field,
        house_number_field,
        name_field,
        lat_field,
        lon_field,
        i_field,
    })
}

pub struct SearchResultItem {
    pub address_string: String,
    pub score: f32,
    pub point: (f64, f64),
    pub geo_type: GeoObjectType,
    pub multipolygon: Option<geo::MultiPolygon>,
}

impl ForwardGeocoder {
    pub fn search(
        &self,
        query_text: String,
        type_filter: Option<GeocodeTypeFilter>,
    ) -> tantivy::Result<Vec<SearchResultItem>> {
        let searcher = self.index.reader()?.searcher();

        let query_parser = {
            let mut query_parser = QueryParser::for_index(
                &self.index,
                vec![
                    self.region_field,
                    self.city_field,
                    self.street_field,
                    self.house_number_field,
                    self.name_field,
                ],
            );

            let text_boost = 20.0;

            query_parser.set_field_fuzzy(self.region_field, true, 1, true);
            query_parser.set_field_boost(self.region_field, text_boost);
            query_parser.set_field_fuzzy(self.city_field, true, 1, true);
            query_parser.set_field_boost(self.city_field, text_boost);
            query_parser.set_field_fuzzy(self.street_field, true, 1, true);
            query_parser.set_field_boost(self.street_field, text_boost);
            // query_parser.set_field_fuzzy(self.house_number_field, true, 1, true);
            query_parser.set_field_fuzzy(self.name_field, true, 1, true);
            query_parser.set_field_boost(self.name_field, text_boost);

            query_parser
        };

        let type_filter = type_filter.unwrap_or_default();

        let query = query_parser.parse_query(&query_text)?;
        let collector = FilterCollector::new(
            "geo_type".to_string(),
            move |t: u64| type_filter.matches(GeoObjectType::try_from(t).unwrap()),
            TopDocs::with_limit(10).order_by_score(),
        );
        let top_docs = searcher.search(&query, &collector)?;

        let mut out: Vec<SearchResultItem> = Vec::new();

        for (score, doc_address) in top_docs {
            let retrieved_doc: TantivyDocument = searcher.doc(doc_address)?;
            let geo_type = GeoObjectType::try_from(
                retrieved_doc
                    .get_first(self.geo_type_field)
                    .unwrap()
                    .as_u64()
                    .unwrap(),
            )
            .unwrap();
            let address_string = vec![
                retrieved_doc
                    .get_first(self.region_field)
                    .map(|f| f.as_str())
                    .unwrap_or_default(),
                retrieved_doc
                    .get_first(self.city_field)
                    .map(|f| f.as_str())
                    .unwrap_or_default(),
                retrieved_doc
                    .get_first(self.street_field)
                    .map(|f| f.as_str())
                    .unwrap_or_default(),
                retrieved_doc
                    .get_first(self.house_number_field)
                    .map(|f| f.as_str())
                    .unwrap_or_default(),
                retrieved_doc
                    .get_first(self.name_field)
                    .map(|f| f.as_str())
                    .unwrap_or_default(),
            ]
            .into_iter()
            .filter_map(|v| v)
            .collect::<Vec<&str>>()
            .join(", ");

            let mp = retrieved_doc
                .get_first(self.i_field)
                .map(|i| i.as_u64())
                .map(|i| i.map(|i| self.cache.zones[i as usize].polygon.clone()))
                .flatten();

            out.push(SearchResultItem {
                address_string,
                score,
                point: (
                    retrieved_doc
                        .get_first(self.lat_field)
                        .unwrap()
                        .as_f64()
                        .unwrap(),
                    retrieved_doc
                        .get_first(self.lon_field)
                        .unwrap()
                        .as_f64()
                        .unwrap(),
                ),
                geo_type,
                multipolygon: mp,
            });
        }

        Ok(out)
    }
}
