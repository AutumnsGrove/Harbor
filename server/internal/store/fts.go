package store

import (
	"errors"
	"strings"
	"unicode"
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
	quoted := make([]string, 0, MaxQueryTerms)
	for _, term := range strings.Fields(input) {
		// A term of only punctuation has nothing to match; drop it rather than fail
		// the whole search for one stray comma. The cap is checked after dropping,
		// not before: sixteen dashes must not spend a budget the real word behind
		// them needed (review on #94).
		if !hasLetterOrDigit(term) {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		if len(quoted) == MaxQueryTerms {
			break
		}
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

// hasLetterOrDigit reports whether a term has anything FTS5 will index. The
// tokenizer keeps letters and digits and drops everything else, so this asks the
// same question of Unicode rather than of ASCII: a search in Japanese or Greek is
// a real search, and an em dash, an arrow or a smart quote is punctuation in any
// script.
//
// The first version treated every rune above 127 as a letter, which let `Text: "—"`
// through validation and then matched nothing — the silent failure the check is
// there to prevent (review on #94).
func hasLetterOrDigit(term string) bool {
	for _, r := range term {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
