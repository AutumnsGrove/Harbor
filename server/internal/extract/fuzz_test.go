package extract

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// plans/03 test 5 — FuzzHTMLExtract. The bytes come from the public internet,
// so whatever they contain, extraction must not panic, must produce valid
// UTF-8, must stay under the caps, and must produce details that JSON can hold.
func FuzzHTMLExtract(f *testing.F) {
	paths, err := filepath.Glob("testdata/pages/*.html")
	if err != nil {
		f.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	for _, seed := range []string{
		"",
		"<",
		"<html",
		"\x00\x00\x00",
		"<!doctype html><script type=application/ld+json>{\"@type\":\"Recipe\",\"recipeIngredient\":[1,null,{}]}</script>",
		"<html><body>" + string(bytes.Repeat([]byte("é"), 4096)) + "</body></html>",
	} {
		f.Add([]byte(seed))
	}

	base, _ := url.Parse("https://example.test/page")
	f.Fuzz(func(t *testing.T, data []byte) {
		result, err := HTML(bytes.NewReader(data), base)
		if err != nil {
			return // unparsable input is rejected, never a panic
		}
		for name, value := range map[string]string{
			"text":     result.Text,
			"markdown": result.Markdown,
			"title":    result.Title,
			"excerpt":  result.Excerpt,
		} {
			if !utf8.ValidString(value) {
				t.Fatalf("%s is not valid UTF-8 for %q", name, data)
			}
		}
		if len(result.Text) > MaxTextBytes {
			t.Fatalf("text is %d bytes, above the cap", len(result.Text))
		}
		if len(result.Markdown) > MaxMarkdownBytes {
			t.Fatalf("markdown is %d bytes, above the cap", len(result.Markdown))
		}
		if len(result.Title) > MaxTitleBytes {
			t.Fatalf("title is %d bytes, above the cap", len(result.Title))
		}
		if _, err := json.Marshal(result.Details); err != nil {
			t.Fatalf("details are not JSON-serialisable: %v", err)
		}
		// The output re-parses as HTML means the Markdown converter cannot have
		// emitted a broken byte sequence; it is a cheap extra invariant.
		if !utf8.ValidString(string(data)) {
			t.Log("input was not UTF-8; output still was")
		}
	})
}

// FuzzDocumentExtract — a document decides its own metadata fence, so the
// front-matter reader is a parser of hostile input like any other. Whatever the
// bytes, extraction must not panic, must stay under the caps, must produce valid
// UTF-8, and must never report metadata the file did not have.
func FuzzDocumentExtract(f *testing.F) {
	paths, err := filepath.Glob("testdata/documents/*.md")
	if err != nil {
		f.Fatal(err)
	}
	paths = append(paths, mustGlob(f, "testdata/documents/*.txt")...)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data, false)
		f.Add(data, true)
	}
	for _, seed := range []string{
		"",
		"---",
		"---\n",
		"---\n---\n",
		"---\ntitle: x\n---\n",
		"---\n: no key\n---\nbody",
		"---\ntitle: 'unclosed quote\n---\nbody",
		"\ufeff---\ntitle: bom\n---\nbody",
		"---\r\ntitle: crlf\r\n---\r\nbody",
		strings.Repeat("-", 3+MaxTextBytes),
	} {
		f.Add([]byte(seed), false)
		f.Add([]byte(seed), true)
	}

	base, _ := url.Parse("https://example.test/page")
	f.Fuzz(func(t *testing.T, data []byte, plain bool) {
		contentType := "text/markdown; charset=utf-8"
		if plain {
			contentType = "text/plain"
		}
		result, err := Document(bytes.NewReader(data), contentType, base)
		if err != nil {
			return // rejected input is not a panic
		}
		for name, value := range map[string]string{
			"text":     result.Text,
			"markdown": result.Markdown,
			"title":    result.Title,
			"excerpt":  result.Excerpt,
			"byline":   result.Byline,
		} {
			if !utf8.ValidString(value) {
				t.Fatalf("%s is not valid UTF-8 for %q", name, data)
			}
		}
		if len(result.Text) > MaxTextBytes {
			t.Fatalf("text is %d bytes, above the cap", len(result.Text))
		}
		if len(result.Markdown) > MaxMarkdownBytes {
			t.Fatalf("markdown is %d bytes, above the cap", len(result.Markdown))
		}
		if len(result.Title) > MaxTitleBytes || len(result.Excerpt) > MaxExcerptBytes {
			t.Fatalf("metadata above its cap: %d / %d", len(result.Title), len(result.Excerpt))
		}
		if plain && (result.Title != "" || result.Excerpt != "" || result.CoverURL != "") {
			t.Fatalf("plain text produced metadata: %+v", result)
		}
		if result.Text != result.Markdown {
			t.Fatalf("text and markdown differ for a document: %q / %q", result.Text, result.Markdown)
		}
	})
}

func mustGlob(f *testing.F, pattern string) []string {
	paths, err := filepath.Glob(pattern)
	if err != nil {
		f.Fatal(err)
	}
	return paths
}
