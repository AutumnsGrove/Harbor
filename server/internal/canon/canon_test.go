package canon

import "testing"

// design.md §5 — "Re-saving is a merge, not a duplicate". Canonicalisation
// decides what "the same page" means: it lowercases the host, strips the
// fragment and tracking parameters, and keeps the rest of the query. These
// tables are the rules; the fuzz and property tests prove they hold for input
// nobody thought of.

func TestCanonicalURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"already canonical", "https://example.com/a/b?x=1", "https://example.com/a/b?x=1"},
		{"lowercase host", "https://ExAmple.COM/Path", "https://example.com/Path"},
		{"lowercase scheme", "HTTPS://example.com/", "https://example.com/"},
		{"path case is kept", "https://example.com/Some/PATH", "https://example.com/Some/PATH"},
		{"strip fragment", "https://example.com/a#section-2", "https://example.com/a"},
		{"strip empty fragment", "https://example.com/a#", "https://example.com/a"},
		{"strip utm params", "https://example.com/a?utm_source=x&utm_medium=y&id=7", "https://example.com/a?id=7"},
		{"strip utm case-insensitively", "https://example.com/a?UTM_Campaign=x&id=7", "https://example.com/a?id=7"},
		{"strip fbclid", "https://example.com/a?fbclid=abc&id=7", "https://example.com/a?id=7"},
		{"strip gclid", "https://example.com/a?id=7&gclid=abc", "https://example.com/a?id=7"},
		{"only tracking leaves no query", "https://example.com/a?utm_source=x&fbclid=y", "https://example.com/a"},
		{"keep meaningful query", "https://example.com/watch?v=abc123&t=30", "https://example.com/watch?v=abc123&t=30"},
		{"query order is kept", "https://example.com/a?b=2&a=1", "https://example.com/a?b=2&a=1"},
		{"param that merely starts with utm is not stripped", "https://example.com/a?utmost=1", "https://example.com/a?utmost=1"},
		{"empty query marker dropped", "https://example.com/a?", "https://example.com/a"},
		{"drop default http port", "http://example.com:80/a", "http://example.com/a"},
		{"drop default https port", "https://example.com:443/a", "https://example.com/a"},
		{"keep non-default port", "https://example.com:8443/a", "https://example.com:8443/a"},
		{"keep http port on https", "https://example.com:80/a", "https://example.com:80/a"},
		{"drop empty port", "https://example.com:/a", "https://example.com/a"},
		{"leading zeros in port", "http://example.com:0080/a", "http://example.com/a"},
		{"empty path becomes slash", "https://example.com", "https://example.com/"},
		{"empty path with query", "https://example.com?id=1", "https://example.com/?id=1"},
		{"trailing slash is kept", "https://example.com/a/", "https://example.com/a/"},
		{"www stays distinct", "https://www.example.com/a", "https://www.example.com/a"},
		{"surrounding whitespace is trimmed", "  https://example.com/a \n", "https://example.com/a"},
		{"unicode host becomes punycode", "https://bücher.example/a", "https://xn--bcher-kva.example/a"},
		{"unicode host is lowercased first", "https://BÜCHER.example/a", "https://xn--bcher-kva.example/a"},
		{"punycode host is left alone", "https://xn--bcher-kva.example/a", "https://xn--bcher-kva.example/a"},
		{"ipv6 host", "http://[::1]:80/a", "http://[::1]/a"},
		{"space in query is escaped so the key survives a trim", "http://0? #", "http://0/?%20"},
		{"percent-encoded path is kept", "https://example.com/a%20b", "https://example.com/a%20b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalURL(tt.raw)
			if err != nil {
				t.Fatalf("CanonicalURL(%q) error = %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("CanonicalURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestCanonicalURLRejects(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"no scheme is never guessed", "example.com/a"},
		{"scheme-relative", "//example.com/a"},
		{"ftp", "ftp://example.com/a"},
		{"javascript", "javascript:alert(1)"},
		{"mailto", "mailto:a@example.com"},
		{"data", "data:text/plain,hi"},
		{"file", "file:///etc/passwd"},
		{"credentials", "https://user:pass@example.com/a"},
		{"username only", "https://user@example.com/a"},
		{"empty host", "https:///a"},
		{"scheme only", "https://"},
		{"port out of range", "https://example.com:99999/a"},
		{"control character", "https://example.com/a\x00b"},
		{"space in host", "https://exa mple.com/a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := CanonicalURL(tt.raw); err == nil {
				t.Errorf("CanonicalURL(%q) = %q, want an error", tt.raw, got)
			}
		})
	}
}

// Known IDNA vectors, so the hand-written encoder is checked against values
// that did not come from itself.
func TestPunycodeKnownVectors(t *testing.T) {
	tests := map[string]string{
		"münchen": "xn--mnchen-3ya",
		"bücher":  "xn--bcher-kva",
		"日本語":     "xn--wgv71a119e",
		"例え":      "xn--r8jz45g",
		"ドメイン名例え": "xn--r8j4j0d1dzdp15t1ug",
		"пример":  "xn--e1afmkfd",
	}
	for in, want := range tests {
		got, err := toASCIILabel(in)
		if err != nil || got != want {
			t.Errorf("toASCIILabel(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
