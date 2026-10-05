package fgeocode

import (
	"fmt"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	"github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/mapping"

	// Register every language analyzer Bleve ships. Each package adds its
	// analyzer to the global registry from init(); without these imports the
	// locale-selected analyzers below would not exist at runtime.
	_ "github.com/blevesearch/bleve/v2/analysis/lang/ar"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/cjk"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/ckb"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/da"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/de"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/en"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/es"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/fa"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/fi"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/fr"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/hi"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/hr"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/hu"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/it"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/nl"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/no"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/pl"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/pt"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/ro"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/ru"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/sv"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/tr"
)

// Document field names. They are shared by the mapping, the indexed documents
// and the query builder.
const (
	fieldAddress         = "address"
	fieldSuggest         = "suggest"
	fieldKind            = "kind"
	fieldHouseNormalized = "house_normalized"
	fieldDisplay         = "display"
	fieldLat             = "lat"
	fieldLon             = "lon"
	fieldZoneIdx         = "zone_idx"
)

// analyzerFallback keeps the surface form of a token: unicode tokenization and
// lowercasing only — no stop words and no stemming. It backs the `suggest` and
// `house` fields, whose terms must remain words a user recognizes.
const analyzerFallback = "default_text"

// languageAnalyzers maps a language subtag that may appear in cache metadata
// (`ru`, `en_US`, ...) to a registered Bleve analyzer. Only languages that
// ship a complete analyzer appear here; anything else falls back to
// analyzerFallback, because Bleve has no stemmer for it.
var languageAnalyzers = map[string]string{
	"ar":  "ar",
	"cjk": "cjk",
	"ckb": "ckb",
	"da":  "da",
	"de":  "de",
	"en":  "en",
	"es":  "es",
	"fa":  "fa",
	"fi":  "fi",
	"fr":  "fr",
	"hi":  "hi",
	"hr":  "hr",
	"hu":  "hu",
	"it":  "it",
	"nl":  "nl",
	"no":  "no",
	"pl":  "pl",
	"pt":  "pt",
	"ro":  "ro",
	"ru":  "ru",
	"sv":  "sv",
	"tr":  "tr",
}

// localeAnalyzer resolves a cache metadata locale to a Bleve analyzer name,
// or "" when the locale should use the non-stemming fallback.
func localeAnalyzer(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if locale == "" || locale == "official" {
		return ""
	}
	if i := strings.IndexAny(locale, "_-"); i >= 0 {
		locale = locale[:i]
	}
	return languageAnalyzers[locale]
}

// buildMapping builds the index mapping used when the index is created fresh.
// The text analyzer is selected from the cache locale; the `suggest` and
// `house` fields always use the non-stemming fallback.
func buildMapping(locale string) (*mapping.IndexMappingImpl, error) {
	m := bleve.NewIndexMapping()
	if err := m.AddCustomAnalyzer(analyzerFallback, map[string]interface{}{
		"type":          custom.Name,
		"tokenizer":     unicode.Name,
		"token_filters": []string{lowercase.Name},
	}); err != nil {
		return nil, fmt.Errorf("register analyzer %q: %w", analyzerFallback, err)
	}

	textAnalyzer := analyzerFallback
	if a := localeAnalyzer(locale); a != "" {
		textAnalyzer = a
	}

	text := bleve.NewTextFieldMapping()
	text.Analyzer = textAnalyzer
	text.Store = false
	text.IncludeTermVectors = false
	for _, field := range []string{"street", "city", "region", "name", fieldAddress} {
		m.DefaultMapping.AddFieldMappingsAt(field, text)
	}

	surface := bleve.NewTextFieldMapping()
	surface.Analyzer = analyzerFallback
	surface.Store = false
	surface.IncludeTermVectors = false
	m.DefaultMapping.AddFieldMappingsAt(fieldSuggest, surface)
	m.DefaultMapping.AddFieldMappingsAt("house", surface)

	houseNormalized := bleve.NewKeywordFieldMapping()
	houseNormalized.Store = false
	m.DefaultMapping.AddFieldMappingsAt(fieldHouseNormalized, houseNormalized)

	kind := bleve.NewKeywordFieldMapping()
	kind.Store = true
	m.DefaultMapping.AddFieldMappingsAt(fieldKind, kind)

	display := bleve.NewTextFieldMapping()
	display.Store = true
	display.Index = false
	display.IncludeTermVectors = false
	m.DefaultMapping.AddFieldMappingsAt(fieldDisplay, display)

	number := bleve.NewNumericFieldMapping()
	number.Store = true
	for _, field := range []string{fieldLat, fieldLon, fieldZoneIdx} {
		m.DefaultMapping.AddFieldMappingsAt(field, number)
	}

	return m, nil
}
