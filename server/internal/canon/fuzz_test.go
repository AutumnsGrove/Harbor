package canon

import (
	"net/url"
	"strings"
	"testing"
)

// design.md §5 — the key must be stable. Untrusted input boundary: every URL
// that reaches the library, from a door or an import, passes through here.
func FuzzCanonicalURL(f *testing.F) {
	seeds := []string{
		"https://example.com/",
		"https://ExAmple.COM:443/a/B?utm_source=x&id=7#frag",
		"http://example.com:80",
		"https://bücher.example/a",
		"https://xn--bcher-kva.example/",
		"https://example.com/a%20b?q=a%26b&fbclid=1",
		"http://[::1]:8080/x",
		"https://user:pass@example.com/",
		"ftp://example.com/",
		"example.com/no-scheme",
		"https://example.com/?&&utm_x=1&&",
		"https://例え.jp/パス?クエリ=値",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got, err := CanonicalURL(raw)
		if err != nil {
			return
		}
		again, err := CanonicalURL(got)
		if err != nil {
			t.Fatalf("CanonicalURL(%q) = %q, which is itself rejected: %v", raw, got, err)
		}
		if again != got {
			t.Fatalf("not idempotent: %q -> %q -> %q", raw, got, again)
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Fatalf("output %q does not parse: %v", got, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			t.Fatalf("output %q has scheme %q", got, u.Scheme)
		}
		if u.User != nil || u.Fragment != "" || strings.Contains(got, "#") {
			t.Fatalf("output %q kept credentials or a fragment", got)
		}
		if u.Host == "" || u.Host != strings.ToLower(u.Host) {
			t.Fatalf("output %q has an empty or mixed-case host", got)
		}
		for _, pair := range strings.Split(u.RawQuery, "&") {
			name := strings.ToLower(strings.SplitN(pair, "=", 2)[0])
			if strings.HasPrefix(name, "utm_") || name == "fbclid" || name == "gclid" {
				t.Fatalf("output %q kept tracking parameter %q", got, pair)
			}
		}
	})
}
