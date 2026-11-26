package store

import (
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
	for _, input := range []string{"", "   ", "---", "...", "!!! ???"} {
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

func TestFTSQueryCapsTheTermCount(t *testing.T) {
	got, err := FTSQuery(strings.TrimSpace(strings.Repeat("word ", MaxQueryTerms+20)))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(strings.Fields(got)); n != MaxQueryTerms {
		t.Errorf("terms = %d, want the cap %d", n, MaxQueryTerms)
	}
}
