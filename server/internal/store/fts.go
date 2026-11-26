package store

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// FTSQuery turns a person's words into an FTS5 MATCH expression.
//
// Every term is quoted, so nothing a user types can be read as FTS5 syntax: a
// stray `NEAR`, `*`, `^`, a `:` column filter or an unbalanced quote is a word
// here, not an operator. Terms are joined by space, which FTS5 reads as AND —
// "long read" finds items carrying both.
//
// This is the one place a search string becomes SQL, which is why the fuzz target
// lives beside it: the property is that the output never contains FTS5 syntax
// outside the quotes it built, and that the argument count is one placeholder.
func FTSQuery(input string) (string, error) {
	terms := strings.Fields(input)
	if len(terms) == 0 {
		return "", ErrEmptyQuery
	}
	if len(terms) > MaxQueryTerms {
		terms = terms[:MaxQueryTerms]
	}

	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		// A term of only punctuation has nothing to match and would be a syntax
		// error even inside quotes in some FTS5 builds; drop it rather than fail
		// the whole search for one stray comma.
		if !hasLetterOrDigit(term) {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	if len(quoted) == 0 {
		return "", ErrEmptyQuery
	}
	return strings.Join(quoted, " "), nil
}

// ErrEmptyQuery is not a failure: an empty search box and a query of pure
// punctuation both mean "nothing to look for".
var ErrEmptyQuery = errors.New("store: nothing to search for")

// MaxQueryTerms bounds a query: past a handful of words the ranking is arbitrary,
// and the cap keeps one search from building a thousand-term expression.
const MaxQueryTerms = 16

// hasLetterOrDigit reports whether a term has anything indexable in it. FTS5's
// tokenizer drops punctuation, so `---` searches for nothing.
func hasLetterOrDigit(term string) bool {
	for _, r := range term {
		if r == utf8.RuneError {
			// A term that is not valid UTF-8 cannot be quoted safely.
			continue
		}
		if ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			return true
		}
		// Letters outside ASCII are indexable too: a search in Japanese or Greek
		// must not be treated as punctuation.
		if r > 127 && (r != '…') {
			return true
		}
	}
	return false
}
