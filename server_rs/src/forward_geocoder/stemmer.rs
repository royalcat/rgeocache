use std::{borrow::Cow, mem};

use frostem::Algorithm;
use tantivy::tokenizer::{Token, TokenFilter, TokenStream, Tokenizer};

// Frostem-based stemmer token filter.
#[derive(Clone)]
pub struct Stemmer {
    stemmer_algorithm: Algorithm,
}

impl Stemmer {
    /// Creates a new `Stemmer` [`TokenFilter`] for a given language algorithm.
    pub fn new(algoritm: frostem::Algorithm) -> Stemmer {
        Stemmer {
            stemmer_algorithm: algoritm,
        }
    }
}

impl Default for Stemmer {
    /// Creates a new `Stemmer` [`TokenFilter`] for [`Language::English`].
    fn default() -> Self {
        Stemmer::new(frostem::Algorithm::English)
    }
}

impl TokenFilter for Stemmer {
    type Tokenizer<T: Tokenizer> = StemmerFilter<T>;

    fn transform<T: Tokenizer>(self, tokenizer: T) -> StemmerFilter<T> {
        StemmerFilter {
            stemmer_algorithm: self.stemmer_algorithm,
            inner: tokenizer,
        }
    }
}

#[derive(Clone)]
pub struct StemmerFilter<T> {
    stemmer_algorithm: Algorithm,
    inner: T,
}

impl<T: Tokenizer> Tokenizer for StemmerFilter<T> {
    type TokenStream<'a> = StemmerTokenStream<T::TokenStream<'a>>;

    fn token_stream<'a>(&'a mut self, text: &'a str) -> Self::TokenStream<'a> {
        let stemmer = frostem::Stemmer::new(self.stemmer_algorithm);
        StemmerTokenStream {
            tail: self.inner.token_stream(text),
            stemmer,
            buffer: String::new(),
        }
    }
}

pub struct StemmerTokenStream<T> {
    tail: T,
    stemmer: frostem::Stemmer,
    buffer: String,
}

impl<T: TokenStream> TokenStream for StemmerTokenStream<T> {
    fn advance(&mut self) -> bool {
        if !self.tail.advance() {
            return false;
        }
        let token = self.tail.token_mut();
        let stemmed_str = self.stemmer.stem(&token.text);
        match stemmed_str {
            Cow::Owned(stemmed_str) => token.text = stemmed_str,
            Cow::Borrowed(stemmed_str) => {
                self.buffer.clear();
                self.buffer.push_str(stemmed_str);
                mem::swap(&mut token.text, &mut self.buffer);
            }
        }
        true
    }

    fn token(&self) -> &Token {
        self.tail.token()
    }

    fn token_mut(&mut self) -> &mut Token {
        self.tail.token_mut()
    }
}
