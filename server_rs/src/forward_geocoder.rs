use tantivy::collector::TopDocs;
use tantivy::query::QueryParser;
use tantivy::schema::*;
use tantivy::{doc, Index, IndexWriter};

use crate::cache::CacheFile;

use tantivy::tokenizer::*;

pub struct ForwardGeocoder {
    index: Index,
    region_field: Field,
    city_field: Field,
    street_field: Field,
    house_number_field: Field,
    name_field: Field,
    lat_field: Field,
    lon_field: Field,
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

pub fn build_geocoder(file: &CacheFile) -> tantivy::Result<ForwardGeocoder> {
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
    let lat_field = schema_builder.add_f64_field("lat", STORED);
    let lon_field = schema_builder.add_f64_field("lon", STORED);
    let schema = schema_builder.build();

    let index = Index::create_in_ram(schema.clone());

    index
        .tokenizers()
        .register(ru_geo_tokinizer_name, ru_geo_tokinizer);

    let mut index_writer: IndexWriter = index.writer(100_000_000)?;

    for p in file.iter_points() {
        index_writer.add_document(doc!(
            region_field => file.read_string(p.data.region_id.get()),
            city_field => file.read_string(p.data.city_id.get()),
            street_field => file.read_string(p.data.street_id.get()),
            house_number_field => file.read_string(p.data.house_number_id.get()),
            name_field => file.read_string(p.data.name_id.get()),
            lat_field => p.lat,
            lon_field => p.lon
        ))?;
    }

    index_writer.commit()?;

    Ok(ForwardGeocoder {
        index,
        region_field,
        city_field,
        street_field,
        house_number_field,
        name_field,
        lat_field,
        lon_field,
    })
}

impl ForwardGeocoder {
    pub fn search(&self, query_text: String) -> tantivy::Result<Vec<(String, f64, f64, f32)>> {
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
            query_parser.set_field_boost(self.name_field, 1.0);

            query_parser
        };

        let query = query_parser.parse_query(&query_text)?;
        let collector = TopDocs::with_limit(10).order_by_score();
        let top_docs = searcher.search(&query, &collector)?;

        let mut out: Vec<(String, f64, f64, f32)> = Vec::new();

        for (_score, doc_address) in top_docs {
            let retrieved_doc: TantivyDocument = searcher.doc(doc_address)?;
            let address_string = vec![
                retrieved_doc
                    .get_first(self.region_field)
                    .unwrap()
                    .as_str()
                    .unwrap(),
                retrieved_doc
                    .get_first(self.city_field)
                    .unwrap()
                    .as_str()
                    .unwrap(),
                retrieved_doc
                    .get_first(self.street_field)
                    .unwrap()
                    .as_str()
                    .unwrap(),
                retrieved_doc
                    .get_first(self.house_number_field)
                    .unwrap()
                    .as_str()
                    .unwrap(),
                retrieved_doc
                    .get_first(self.name_field)
                    .unwrap()
                    .as_str()
                    .unwrap(),
            ]
            .into_iter()
            .filter(|a| !a.is_empty())
            .collect::<Vec<&str>>()
            .join(", ");
            out.push((
                address_string,
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
                _score,
            ));
        }

        Ok(out)
    }
}
