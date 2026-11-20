package canon

import (
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// A generated page: the parts that make two spellings "the same page".
type page struct {
	host, path, query string // query holds only meaningful params
	https             bool
}

func pageGen() *rapid.Generator[page] {
	label := rapid.StringMatching(`[a-z][a-z0-9]{0,8}`)
	return rapid.Custom(func(t *rapid.T) page {
		host := label.Draw(t, "label") + "." + rapid.SampledFrom([]string{"com", "org", "example"}).Draw(t, "tld")
		if rapid.Bool().Draw(t, "www") {
			host = "www." + host
		}
		path := "/" + rapid.StringMatching(`[A-Za-z0-9_-]{0,8}(/[A-Za-z0-9_-]{1,6}){0,2}`).Draw(t, "path")
		params := rapid.SliceOfN(rapid.StringMatching(`[a-z][a-z0-9]{0,4}=[a-z0-9]{0,5}`), 0, 3).Draw(t, "params")
		return page{host: host, path: path, query: strings.Join(params, "&"), https: rapid.Bool().Draw(t, "https")}
	})
}

// spell renders a page as one of the many ways a person might have pasted it.
func spell(t *rapid.T, p page) string {
	scheme, port := "http", ":80"
	if p.https {
		scheme, port = "https", ":443"
	}
	host := p.host
	if rapid.Bool().Draw(t, "upperHost") {
		host = strings.ToUpper(host)
	}
	if rapid.Bool().Draw(t, "defaultPort") {
		host += port
	}
	params := []string{}
	if p.query != "" {
		params = strings.Split(p.query, "&")
	}
	for _, tracker := range rapid.SliceOfN(rapid.SampledFrom([]string{"utm_source=a", "UTM_medium=b", "fbclid=c", "gclid=d"}), 0, 3).Draw(t, "trackers") {
		at := rapid.IntRange(0, len(params)).Draw(t, "at")
		params = append(params[:at], append([]string{tracker}, params[at:]...)...)
	}
	query := strings.Join(params, "&")
	out := scheme + "://" + host + p.path
	if query != "" {
		out += "?" + query
	}
	if rapid.Bool().Draw(t, "fragment") {
		out += "#" + rapid.StringMatching(`[a-z0-9-]{0,8}`).Draw(t, "frag")
	}
	return out
}

// plans/02 test 1 — idempotence over generated URLs.
func TestPropertyCanonicalURLIsIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		raw := spell(t, pageGen().Draw(t, "page"))
		once, err := CanonicalURL(raw)
		if err != nil {
			t.Fatalf("CanonicalURL(%q): %v", raw, err)
		}
		twice, err := CanonicalURL(once)
		if err != nil || twice != once {
			t.Fatalf("%q -> %q -> %q, %v", raw, once, twice, err)
		}
	})
}

// design.md §5 — two spellings of the same page (case, tracking params,
// fragment, default port) produce one key.
func TestPropertyTwoSpellingsOneKey(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := pageGen().Draw(t, "page")
		a, b := spell(t, p), spell(t, p)
		ka, err := CanonicalURL(a)
		if err != nil {
			t.Fatalf("CanonicalURL(%q): %v", a, err)
		}
		kb, err := CanonicalURL(b)
		if err != nil {
			t.Fatalf("CanonicalURL(%q): %v", b, err)
		}
		if ka != kb {
			t.Fatalf("%q and %q are the same page but keys differ: %q vs %q", a, b, ka, kb)
		}
	})
}

// plans/02 test 1 — "www." is a different site, so it is a different key.
func TestPropertyWWWStaysDistinct(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := pageGen().Draw(t, "page")
		p.host = strings.TrimPrefix(p.host, "www.")
		bare, err1 := CanonicalURL(spell(t, p))
		p.host = "www." + p.host
		www, err2 := CanonicalURL(spell(t, p))
		if err1 != nil || err2 != nil || bare == www {
			t.Fatalf("bare=%q www=%q errs=%v %v", bare, www, err1, err2)
		}
	})
}
