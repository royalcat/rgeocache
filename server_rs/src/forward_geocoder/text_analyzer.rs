use std::collections::HashMap;
use std::str::FromStr;
use std::sync::Arc;

use geo::Centroid;
use strum::EnumString;
use tantivy::collector::{FilterCollector, TopDocs};
use tantivy::query::{BooleanQuery, BoostQuery, FuzzyTermQuery, Occur, Query, TermQuery};
use tantivy::schema::*;
use tantivy::tokenizer::*;
use tantivy::{
    doc, DocAddress, Index, IndexReader, IndexWriter, Score, Searcher, TantivyDocument,
    TantivyError, Term,
};

use crate::cache::{CacheFile, IndexedZone};

#[derive(Clone)]
pub struct Analyzers {
    pub language: Option<Language>,
    pub text: TextAnalyzer,
    pub house: TextAnalyzer,
}

pub fn build_analyzers(locale: &str) -> Analyzers {
    let stemmer = resolve_stemmer(locale);

    let mut text_builder = TextAnalyzer::builder(SimpleTokenizer::default())
        .filter(LowerCaser)
        .filter(YoNormalizer)
        // No-op for Cyrillic (the folding table has no U+0400..U+04FF entries),
        // useful for Latin locales. Never add AlphaNumOnlyFilter here — it
        // would delete every Cyrillic token.
        .filter(AsciiFoldingFilter)
        .filter(RemoveLongFilter::limit(64))
        .dynamic();
    if let Some(language) = stemmer {
        text_builder = text_builder.filter_dynamic(Stemmer::new(language));
    }

    Analyzers {
        language: stemmer,
        text: text_builder.build(),
        house: TextAnalyzer::builder(SimpleTokenizer::default())
            .filter(LowerCaser)
            .filter(RemoveLongFilter::limit(32))
            .build(),
    }
}

/// Map a cache locale (`"ru"`, `"en-US"`, …) onto a snowball stemmer.
///
/// Returns `None` — meaning "lowercase only, no stemming" — for an empty or
/// `"official"` locale and for languages tantivy has no algorithm for
/// (`uk`, `kk`, `be`, …). Guessing English there would stem Cyrillic tokens
/// with Latin rules, which is strictly worse than not stemming at all.
fn resolve_stemmer(locale: &str) -> Option<Language> {
    let lang = locale
        .trim()
        .to_ascii_lowercase()
        .split(['-', '_'])
        .next()
        .unwrap_or("")
        .to_string();

    match lang.as_str() {
        "ar" => Some(Language::Arabic),
        "da" => Some(Language::Danish),
        "de" => Some(Language::German),
        "el" => Some(Language::Greek),
        "en" => Some(Language::English),
        "es" => Some(Language::Spanish),
        "fi" => Some(Language::Finnish),
        "fr" => Some(Language::French),
        "hu" => Some(Language::Hungarian),
        "it" => Some(Language::Italian),
        "nl" => Some(Language::Dutch),
        "no" | "nb" | "nn" => Some(Language::Norwegian),
        "pt" => Some(Language::Portuguese),
        "ro" => Some(Language::Romanian),
        "ru" => Some(Language::Russian),
        "sv" => Some(Language::Swedish),
        "ta" => Some(Language::Tamil),
        "tr" => Some(Language::Turkish),
        _ => None,
    }
}

/// Token filter normalising Russian `ё`/`Ё` to `е`/`Е`.
///
/// OSM spellings are inconsistent (`Королёв` vs `Королев`) and the snowball
/// Russian rules are written against `е`, so neither `LowerCaser` nor
/// `AsciiFoldingFilter` merges the two. Normalising here is cheaper and more
/// reliable than relying on fuzzy edit distance to paper over it.
#[derive(Clone)]
pub struct YoNormalizer;

impl TokenFilter for YoNormalizer {
    type Tokenizer<T: Tokenizer> = YoNormalizerFilter<T>;

    fn transform<T: Tokenizer>(self, tokenizer: T) -> Self::Tokenizer<T> {
        YoNormalizerFilter {
            inner: tokenizer,
            buffer: String::new(),
        }
    }
}

#[derive(Clone)]
pub struct YoNormalizerFilter<T> {
    inner: T,
    buffer: String,
}

impl<T: Tokenizer> Tokenizer for YoNormalizerFilter<T> {
    type TokenStream<'a> = YoNormalizerTokenStream<'a, T::TokenStream<'a>>;

    fn token_stream<'a>(&'a mut self, text: &'a str) -> Self::TokenStream<'a> {
        self.buffer.clear();
        YoNormalizerTokenStream {
            inner: self.inner.token_stream(text),
            buffer: &mut self.buffer,
        }
    }
}

pub struct YoNormalizerTokenStream<'a, T> {
    inner: T,
    buffer: &'a mut String,
}

impl<T: TokenStream> TokenStream for YoNormalizerTokenStream<'_, T> {
    fn advance(&mut self) -> bool {
        if !self.inner.advance() {
            return false;
        }
        // `contains` on a char array scans for either letter; only tokens that
        // actually carry a `ё` pay for the rebuild.
        if self.inner.token().text.contains(['ё', 'Ё']) {
            self.buffer.clear();
            for c in self.inner.token().text.chars() {
                self.buffer.push(match c {
                    'ё' => 'е',
                    'Ё' => 'Е',
                    other => other,
                });
            }
            std::mem::swap(&mut self.inner.token_mut().text, self.buffer);
        }
        true
    }

    fn token(&self) -> &Token {
        self.inner.token()
    }

    fn token_mut(&mut self) -> &mut Token {
        self.inner.token_mut()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolves_locale_to_stemmer() {
        assert_eq!(resolve_stemmer("ru"), Some(Language::Russian));
        assert_eq!(resolve_stemmer("ru-RU"), Some(Language::Russian));
        assert_eq!(resolve_stemmer("ru_RU"), Some(Language::Russian));
        assert_eq!(resolve_stemmer("EN"), Some(Language::English));
        // Unsupported / absent locales must not be guessed at.
        assert_eq!(resolve_stemmer(""), None);
        assert_eq!(resolve_stemmer("official"), None);
        assert_eq!(resolve_stemmer("uk"), None);
        assert_eq!(resolve_stemmer("kk"), None);
    }
}
