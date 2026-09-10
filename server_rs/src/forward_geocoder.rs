use tantivy::collector::TopDocs;
use tantivy::query::QueryParser;
use tantivy::schema::*;
use tantivy::{doc, Index, IndexWriter, ReloadPolicy};

use crate::cache::CacheFile;

pub struct ForwardGeocoder {
    index: Index,
    schema: Schema,
    address_field: Field,
    lat_field: Field,
    lon_field: Field,
}

pub fn build_geocoder(file: &CacheFile) -> tantivy::Result<ForwardGeocoder> {
    let mut schema_builder = Schema::builder();
    let address_field = schema_builder.add_text_field("address", TEXT | STORED);
    let lat_field = schema_builder.add_f64_field("lat", STORED);
    let lon_field = schema_builder.add_f64_field("lon", STORED);
    let schema = schema_builder.build();

    let index = Index::create_in_ram(schema.clone());

    let mut index_writer: IndexWriter = index.writer(100_000_000)?;

    for p in file.iter_points() {
        let address = vec![
            file.read_string(p.data.region_id.get()),
            file.read_string(p.data.city_id.get()),
            file.read_string(p.data.street_id.get()),
            file.read_string(p.data.house_number_id.get()),
            file.read_string(p.data.name_id.get()),
        ]
        .into_iter()
        .filter(|a| !a.is_empty())
        .collect::<Vec<String>>()
        .join(", ");
        index_writer.add_document(doc!(
            address_field => address,
            lat_field => p.lat,
            lon_field => p.lon
        ))?;
    }

    index_writer.commit()?;

    Ok(ForwardGeocoder {
        index,
        schema,
        address_field,
        lat_field,
        lon_field,
    })
}

impl ForwardGeocoder {
    pub fn search(&self, query_text: String) -> tantivy::Result<Vec<(String, f64, f64, f32)>> {
        let searcher = self.index.reader()?.searcher();

        let query_parser = QueryParser::for_index(&self.index, vec![self.address_field]);
        let query = query_parser.parse_query(&query_text)?;
        let collector = TopDocs::with_limit(10).order_by_score();
        let top_docs = searcher.search(&query, &collector)?;

        let mut out: Vec<(String, f64, f64, f32)> = Vec::new();

        for (_score, doc_address) in top_docs {
            let retrieved_doc: TantivyDocument = searcher.doc(doc_address)?;
            out.push((
                retrieved_doc
                    .get_first(self.address_field)
                    .unwrap()
                    .as_str()
                    .unwrap()
                    .to_owned(),
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
