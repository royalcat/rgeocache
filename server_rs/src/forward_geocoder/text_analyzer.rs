use super::stemmer::Stemmer;
use frostem::Algorithm;
use tantivy::tokenizer::*;

#[derive(Clone)]
pub struct Analyzers {
    stemmer_algorithm: Option<Algorithm>,
    pub text: TextAnalyzer,
    pub house: TextAnalyzer,
}

impl Analyzers {
    pub fn stemmer_language(&self) -> Option<String> {
        self.stemmer_algorithm.map(|a| a.name().to_string())
    }
}

pub fn build_analyzers(locale: &str) -> Analyzers {
    let stemmer_algorithm = resolve_stemmer_algorithm(locale);

    let mut text_builder = TextAnalyzer::builder(SimpleTokenizer::default())
        .filter(LowerCaser)
        // No-op for Cyrillic (the folding table has no U+0400..U+04FF entries),
        // useful for Latin locales. Never add AlphaNumOnlyFilter here — it
        // would delete every Cyrillic token.
        .filter(AsciiFoldingFilter)
        .filter(RemoveLongFilter::limit(64))
        .dynamic();
    if let Some(language) = stemmer_algorithm {
        text_builder = text_builder.filter_dynamic(Stemmer::new(language));
    }

    Analyzers {
        stemmer_algorithm,
        text: text_builder.build(),
        house: TextAnalyzer::builder(SimpleTokenizer::default())
            .filter(LowerCaser)
            .filter(RemoveLongFilter::limit(32))
            .build(),
    }
}

fn resolve_stemmer_algorithm(locale: &str) -> Option<Algorithm> {
    let lang = locale
        .trim()
        .to_ascii_lowercase()
        .split(['-', '_'])
        .next()
        .unwrap_or("")
        .to_string();

    Algorithm::from_name(&lang).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolves_locale_to_stemmer() {
        assert_eq!(resolve_stemmer_algorithm("ru"), Some(Algorithm::Russian));
        assert_eq!(resolve_stemmer_algorithm("ru-RU"), Some(Algorithm::Russian));
        assert_eq!(resolve_stemmer_algorithm("ru_RU"), Some(Algorithm::Russian));
        assert_eq!(resolve_stemmer_algorithm("EN"), Some(Algorithm::English));
        // Unsupported / absent locales must not be guessed at.
        assert_eq!(resolve_stemmer_algorithm(""), None);
        assert_eq!(resolve_stemmer_algorithm("official"), None);
        assert_eq!(resolve_stemmer_algorithm("uk"), None);
        assert_eq!(resolve_stemmer_algorithm("kk"), None);
    }
}
