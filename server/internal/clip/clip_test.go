package clip

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestText(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		max   int
		want  string
		exact bool // the result is the whole input, untouched
	}{
		{"empty", "", 10, "", true},
		{"fits", "hello", 10, "hello", true},
		{"fits exactly", "hello", 5, "hello", true},
		{"trims", "  hello  ", 10, "hello", true},
		{"ascii over", strings.Repeat("a", 30), 10, "aaaaaaa…", false},
		{"one over", "abcdef", 5, "ab…", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Text(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("Text(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("not valid UTF-8: %q", got)
			}
		})
	}
}

// The property that matters, over every rune width and every budget: the result
// fits the budget, it is valid UTF-8, and it is a prefix of the input.
func TestTextIsAlwaysBoundedAndValid(t *testing.T) {
	inputs := map[string]string{
		"ascii":      strings.Repeat("a", 200),
		"two-byte":   strings.Repeat("é", 100),
		"three-byte": strings.Repeat("→", 80),
		"four-byte":  strings.Repeat("😀", 60),
		"mixed":      strings.Repeat("aé→😀", 40),
	}
	for name, in := range inputs {
		for max := 0; max <= 24; max++ {
			got := Text(in, max)
			if len(got) > max {
				t.Fatalf("%s at max %d: %d bytes, above the budget: %q", name, max, len(got), got)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("%s at max %d: not valid UTF-8: %q", name, max, got)
			}
			if !strings.HasPrefix(in, strings.TrimSuffix(got, ellipsis)) {
				t.Fatalf("%s at max %d: the result is not a prefix of the input: %q", name, max, got)
			}
		}
	}
}

// A budget too small for the ellipsis still respects the budget rather than
// spending three bytes it does not have.
func TestTinyBudgetSaysNothingAboutBeingCut(t *testing.T) {
	for max := 0; max < len(ellipsis); max++ {
		got := Text(strings.Repeat("a", 10), max)
		if len(got) > max {
			t.Errorf("max %d: %d bytes", max, len(got))
		}
		if strings.Contains(got, ellipsis) {
			t.Errorf("max %d: the ellipsis fit when it could not: %q", max, got)
		}
	}
}

func TestTextDoesNotSplitARune(t *testing.T) {
	// A three-byte rune starting at byte 0: budgets 1 and 2 cannot hold it, so the
	// result is empty rather than a broken byte.
	if got := Text("→→→", 1); got != "" {
		t.Errorf("Text(→→→, 1) = %q, want empty", got)
	}
	// "a" then a three-byte rune: a budget of 5 leaves room for "a" and the
	// ellipsis, not the rune.
	if got := Text("a→→→", 5); got != "a…" {
		t.Errorf("Text(a→→→, 5) = %q, want %q", got, "a…")
	}
}
