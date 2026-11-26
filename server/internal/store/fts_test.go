package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A search string is a person's words, never syntax. Every term is quoted, so the
// FTS5 operators in the table below are words to look for rather than instructions.
func TestFTSQueryQuotesEveryTerm(t *testing.T) {
	cases := map[string]string{
		"pancakes":        `"pancakes"`,
		"long read":       `"long" "read"`,
		"  spaced   out ": `"spaced" "out"`,
		"NEAR":            `"NEAR"`,
		"a*":              `"a*"`,
		"caret^":          `"caret^"`,
		"tag:rust":        `"tag:rust"`,
		`quote"inside`:    `"quote""inside"`,
		"long-read":       `"long-read"`,
		"日本語":             `"日本語"`,
		"café":            `"café"`,
	}
	for input, want := range cases {
		got, err := FTSQuery(input)
		if err != nil {
			t.Errorf("FTSQuery(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("FTSQuery(%q) = %s, want %s", input, got, want)
		}
	}
}

// An empty box and a query of pure punctuation both mean "nothing to look for",
// which is not an error to show.
func TestFTSQueryRefusesNothingToSearchFor(t *testing.T) {
	for _, input := range []string{
		"", "   ", "---", "...", "!!! ???",
		// Non-ASCII punctuation is punctuation: FTS5 drops it in every script, so a
		// term made of it searches for nothing (review on #94).
		"—", "“”", "→ ←", "· •", "…",
	} {
		if got, err := FTSQuery(input); err == nil {
			t.Errorf("FTSQuery(%q) = %q, want ErrEmptyQuery", input, got)
		}
	}
}

// The output is either empty or made only of quoted terms: no bare operator can
// survive, which is what makes one placeholder safe.
func TestFTSQueryOutputIsOnlyQuotedTerms(t *testing.T) {
	for _, input := range []string{"a AND b", `x" OR "y`, "NEAR/3(z)", "*", "a:b", "a OR OR b"} {
		got, err := FTSQuery(input)
		if err != nil {
			continue
		}
		for _, term := range strings.Fields(got) {
			if !strings.HasPrefix(term, `"`) {
				t.Errorf("FTSQuery(%q) = %s: %q is not a quoted term", input, got, term)
			}
		}
	}
}

// Letters and digits in any script are indexable, which is why the check cannot be
// an ASCII range.
func TestFTSQueryKeepsNonLatinWords(t *testing.T) {
	for input, want := range map[string]string{
		"日本語 検索":          `"日本語" "検索"`,
		"griechisch άλφα": `"griechisch" "άλφα"`,
		"accentué":        `"accentué"`,
		"числа 42":        `"числа" "42"`,
		// Numbers that are not Nd: FTS5's tokenizer keeps all of N*, so these are
		// searchable and must not be mistaken for punctuation.
		"½ cup": `"½" "cup"`,
		"²":     `"²"`,
		"Ⅷ":     `"Ⅷ"`,
	} {
		got, err := FTSQuery(input)
		if err != nil {
			t.Errorf("FTSQuery(%q): %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("FTSQuery(%q) = %s, want %s", input, got, want)
		}
	}
}

func TestFTSQueryCapsTheTermCount(t *testing.T) {
	got, err := FTSQuery(strings.TrimSpace(strings.Repeat("word ", MaxQueryTerms+20)))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(got)); n != MaxQueryTerms {
		t.Errorf("terms = %d, want the cap %d", n, MaxQueryTerms)
	}
}

// Punctuation-only terms are dropped before the cap is applied, so noise cannot
// spend a budget a real word needed (review on #94).
func TestFTSQueryNoiseDoesNotEatTheTermBudget(t *testing.T) {
	input := strings.Repeat("- ", MaxQueryTerms+4) + "venice"
	got, err := FTSQuery(input)
	if err != nil {
		t.Fatalf("FTSQuery(%q): %v", input, err)
	}
	if got != `"venice"` {
		t.Errorf("FTSQuery with leading noise = %s, want the real term", got)
	}

	// Real words still stop at the cap, noise or not.
	mixed := "venice " + strings.Repeat("- ", 5) + strings.Repeat("word ", MaxQueryTerms+4)
	got, err = FTSQuery(mixed)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(got)); n != MaxQueryTerms {
		t.Errorf("terms = %d, want the cap %d", n, MaxQueryTerms)
	}
}

// The tokenizer's question, answered by the tokenizer itself: an FTS5 table indexed
// `½` and `Ⅷ` as tokens, so a search for them must be allowed through. The
// alternative — refusing them as punctuation — is the mirror of the bug this check
// exists to prevent (review on #94).
func TestFTSQueryAgreesWithTheTokenizer(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fts.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE t USING fts5(body)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t VALUES('add ½ cup, area ², chapter Ⅷ, an em dash — and an arrow →')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, term := range []string{"½", "²", "Ⅷ", "cup", "dash"} {
		query, err := FTSQuery(term)
		if err != nil {
			t.Errorf("FTSQuery(%q) = %v: the tokenizer indexes it", term, err)
			continue
		}
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM t WHERE t MATCH ?`, query).Scan(&n); err != nil {
			t.Errorf("matching %q: %v", term, err)
			continue
		}
		if n == 0 {
			t.Errorf("FTSQuery(%q) = %s matched nothing, but the tokenizer indexed it", term, query)
		}
	}

	// And the other direction: punctuation really is refused.
	for _, term := range []string{"—", "→", "…"} {
		if _, err := FTSQuery(term); !errors.Is(err, ErrEmptyQuery) {
			t.Errorf("FTSQuery(%q) = %v, want ErrEmptyQuery", term, err)
		}
	}
}
