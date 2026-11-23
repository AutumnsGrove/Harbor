package canon

import (
	"strings"
	"testing"
)

// The golden table is the merge key's contract: what every URL that Harbor has
// already saved canonicalises to today. It was captured from the hand-rolled
// RFC 3492 encoder before golang.org/x/net/idna replaced it (issue #20), and it
// must not be edited to make a different implementation pass — a row that moves
// is a finding to report, because a moved key either splits one page into two
// items or merges two different pages into one (design.md §5).
//
// An empty want means CanonicalURL must refuse the input. A refused URL is not
// a key: nothing is stored, so nothing can silently duplicate.
type goldenKey struct {
	name string
	raw  string
	want string
}

var goldenKeys = []goldenKey{
	// The unicode host and its already-encoded twin are one key.
	{"unicode host becomes punycode", "https://bücher.example/a", "https://xn--bcher-kva.example/a"},
	{"punycode host is left alone", "https://xn--bcher-kva.example/a", "https://xn--bcher-kva.example/a"},
	{"uppercase punycode host is only lowercased", "https://XN--BCHER-KVA.example/", "https://xn--bcher-kva.example/"},
	{"unicode host is lowercased first", "https://BÜCHER.example/a", "https://xn--bcher-kva.example/a"},

	// Known IDNA vectors, so the table does not only agree with itself.
	{"münchen", "https://münchen.example/", "https://xn--mnchen-3ya.example/"},
	{"japanese host", "https://日本語.jp/", "https://xn--wgv71a119e.jp/"},
	{"japanese host with unicode path and query", "https://例え.jp/パス?クエリ=値", "https://xn--r8jz45g.jp/%E3%83%91%E3%82%B9?クエリ=値"},
	{"cyrillic host", "https://пример.example/", "https://xn--e1afmkfd.example/"},
	{"long japanese label", "https://ドメイン名例え.example/", "https://xn--r8j4j0d1dzdp15t1ug.example/"},

	// ASCII passes through byte-for-byte: no mapping, no case folding beyond
	// the lowercase above, no trailing dot or empty label removed.
	{"ascii host pass-through", "https://example.com/a", "https://example.com/a"},
	{"hyphen-run label is kept", "https://r3---sn-apo3qvuoxuxbt-j5pe.googlevideo.com/", "https://r3---sn-apo3qvuoxuxbt-j5pe.googlevideo.com/"},
	{"uppercase ascii host", "https://ExAmple.COM/Path", "https://example.com/Path"},
	{"trailing dot is kept", "https://example.com./a", "https://example.com./a"},
	{"empty label is kept", "https://example..com/a", "https://example..com/a"},
	{"underscore label is kept", "https://_dmarc.example.com/a", "https://_dmarc.example.com/a"},
	{"leading empty label is kept", "https://.example.com/a", "https://.example.com/a"},

	// Literals.
	{"ipv6 literal", "http://[::1]:80/a", "http://[::1]/a"},
	{"ipv6 literal with a full address", "http://[2001:db8::1]/a", "http://[2001:db8::1]/a"},
	{"ipv4 literal", "http://127.0.0.1/a", "http://127.0.0.1/a"},
	{"ipv4 literal with a non-default port", "http://192.168.0.1:8080/a", "http://192.168.0.1:8080/a"},

	// Default ports are removed, others are kept, leading zeros are not a key.
	{"default http port is dropped", "http://example.com:80/a", "http://example.com/a"},
	{"default https port is dropped", "https://example.com:443/a", "https://example.com/a"},
	{"port 0 is kept", "https://example.com:0/a", "https://example.com:0/a"},
	{"non-default port is kept", "https://example.com:8443/a", "https://example.com:8443/a"},
	{"leading zeros in a default port are dropped", "http://example.com:0080/a", "http://example.com/a"},
	{"out-of-range port is refused", "https://example.com:65536/a", ""},
	{"negative port is refused", "https://example.com:-1/a", ""},

	// The cases the fuzzer caught.
	{"percent escape in the host is refused", "http://exa%25mple.com/a", ""},
	{"space in the query is escaped", "http://0? #", "http://0/?%20"},
	{"ipv6 zone id is refused", "http://[fe80::1%25eth0]/a", ""},

	// UTS 46 would move these keys. They are here because the profile choice
	// for the library was made by test: idna.Lookup maps "faß" to "fass",
	// fullwidth letters to ASCII and combining sequences to NFC, and every one
	// of those mappings would re-key a page that is already saved.
	{"deviation character is punycoded, not mapped", "https://faß.example/", "https://xn--fa-hia.example/"},
	{"fullwidth letters are lowercased then punycoded, not mapped", "https://ＡＢＣ.example/", "https://xn--mi7ccd.example/"},
	{"combining sequence is punycoded, not normalised", "https://e\u0301xample.com/", "https://xn--example-tge.com/"},
	{"snowman", "https://☃.example/", "https://xn--n3h.example/"},

	// The rest of the key: fragment, tracking, path shape.
	{"fragment and tracking are dropped", "https://example.com/a?utm_source=x&id=7#frag", "https://example.com/a?id=7"},
	{"empty path becomes a slash", "https://example.com", "https://example.com/"},
	{"trailing slash on a path is kept", "https://example.com/a/", "https://example.com/a/"},
	{"www stays distinct", "https://www.example.com/a", "https://www.example.com/a"},
}

func TestGoldenKeys(t *testing.T) {
	for _, tt := range goldenKeys {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalURL(tt.raw)
			if tt.want == "" {
				if err == nil {
					t.Fatalf("CanonicalURL(%q) = %q, want an error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalURL(%q) error = %v, want %q", tt.raw, err, tt.want)
			}
			if got != tt.want {
				t.Errorf("CanonicalURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// Length is deliberately not a key rule: the hand-rolled encoder had no 63- or
// 255-byte limit, so neither may a library profile that enables VerifyDNSLength
// without changing what Harbor accepts.
func TestGoldenKeysOverlongNames(t *testing.T) {
	label63 := strings.Repeat("aa", 31) + "a"
	label64 := label63 + "a"
	long := strings.Repeat(strings.Repeat("b", 60)+".", 64) + "example"

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"63-byte label is kept", "https://" + label63 + ".example/", "https://" + label63 + ".example/"},
		{"64-byte label is kept", "https://" + label64 + ".example/", "https://" + label64 + ".example/"},
		// 69 bytes once encoded, so this row is why VerifyDNSLength stays off.
		{"unicode label over 63 bytes once punycoded is kept", "https://" + strings.Repeat("ü", 63) + ".example/", "https://xn--tda" + strings.Repeat("a", 62) + ".example/"},
		{"name over 255 bytes is kept", "https://" + long + "/", "https://" + long + "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalURL(tt.raw)
			if err != nil {
				t.Fatalf("CanonicalURL(%q) error = %v, want %q", tt.raw, err, tt.want)
			}
			if got != tt.want {
				t.Errorf("CanonicalURL(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// Two inputs that used to produce a key and are now refused. Neither is a key
// move: CanonicalURL returns an error, nothing is stored, so no saved page can
// split or merge by accident. They are here because issue #20 asked for every
// input whose treatment changed to be recorded with what it used to be.
//
// Both were punycoded whole by the hand-rolled encoder, which produced an
// address no resolver would ever emit:
//
//	xn--bücher (a non-ASCII label with the ACE prefix) -> xn--xn--bcher-u9a
//	U+FFFD (not a domain character)                    -> xn--zn7c
//
// idna treats the first as an A-label and decodes its suffix (a different key,
// not an error), and refuses the second outright. The guard in toASCIILabel
// turns the first into a refusal too, so the only direction either can move is
// to "not saved".
func TestRefusedThatUsedToBeAKey(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		oldKey string
	}{
		{"non-ASCII label with the ACE prefix", "https://xn--bücher.example/", "https://xn--xn--bcher-u9a.example/"},
		{"replacement character in the host", "https://\uFFFD.example/", "https://xn--zn7c.example/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalURL(tt.raw)
			if err == nil {
				t.Fatalf("CanonicalURL(%q) = %q, want an error (the pre-#20 key was %q)", tt.raw, got, tt.oldKey)
			}
		})
	}
}
