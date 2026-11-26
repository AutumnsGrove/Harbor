// Package clip cuts text to a byte budget without splitting a rune.
//
// It exists because the same three lines were written in three packages and the
// same bug came with them: a cut at a byte offset lands in the middle of a
// multi-byte rune, and an ellipsis appended afterwards makes the result longer
// than the budget it was supposed to respect. Both are silent — the string is
// valid enough for a database and wrong in the UI — so the rule lives in one place
// with one test (issue #90).
package clip

import (
	"strings"
	"unicode/utf8"
)

// ellipsis is three bytes, which is why it cannot simply be added to the budget.
const ellipsis = "…"

// Text returns s, trimmed, cut to at most max bytes.
//
// When something is removed the result ends with an ellipsis that counts toward
// max, and the cut lands on a rune boundary so the result is always valid UTF-8.
// A max too small to hold the ellipsis is still respected: the text is cut to it
// and the elision goes unsaid.
func Text(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if room := max - len(ellipsis); room > 0 {
		return cut(s, room) + ellipsis
	}
	return cut(s, max)
}

// cut returns at most max bytes of s, ending on a rune boundary.
func cut(s string, max int) string {
	if max >= len(s) {
		return s
	}
	at := max
	for at > 0 && !utf8.RuneStart(s[at]) {
		at--
	}
	return s[:at]
}
