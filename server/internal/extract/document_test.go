package extract

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// documentFixtures are the pages that are not HTML: a real Markdown capture with
// front matter, a plain text file, and the shapes that must not be mistaken for
// metadata (issue #73).
var documentBases = map[string]string{
	"gwern":             "https://gwern.net/blog/2026/make-me-care",
	"crlf":              "https://example.test/quoted",
	"unterminated":      "https://example.test/rule",
	"front-matter-only": "https://example.test/meta-only",
	"plain":             "https://example.test/notes.txt",
}

var updateDocuments = flag.Bool("update-documents", false, "rewrite the document golden files")

func documentNames(t *testing.T) []string {
	t.Helper()
	patterns := []string{"testdata/documents/*.md", "testdata/documents/*.txt"}
	names := []string{}
	for _, pattern := range patterns {
		paths, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			names = append(names, strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		}
	}
	if len(names) == 0 {
		t.Fatal("no document fixtures found")
	}
	return names
}

func documentFixture(t *testing.T, name string) *Result {
	t.Helper()
	path := filepath.Join("testdata", "documents", name+".md")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join("testdata", "documents", name+".txt")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening fixture %s: %v", name, err)
	}
	defer file.Close()

	raw, ok := documentBases[name]
	if !ok {
		t.Fatalf("fixture %s has no base URL; add one to documentBases", name)
	}
	base, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("base URL for %s: %v", name, err)
	}
	contentType := "text/markdown; charset=utf-8"
	if strings.HasSuffix(path, ".txt") {
		contentType = "text/plain; charset=utf-8"
	}
	result, err := Document(file, contentType, base)
	if err != nil {
		t.Fatalf("extracting %s: %v", name, err)
	}
	return result
}

// The golden files are the record of what each shape produces, and the reason a
// later change to front-matter handling is visible instead of silently different.
func TestDocumentFixtures(t *testing.T) {
	for _, name := range documentNames(t) {
		t.Run(name, func(t *testing.T) {
			result := documentFixture(t, name)
			got, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				t.Fatalf("marshalling the result: %v", err)
			}
			got = append(got, '\n')

			path := filepath.Join("testdata", "documents", "golden", name+".json")
			if *updateDocuments {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("writing %s: %v", path, err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s (run with -update-documents to create it): %v", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("golden mismatch for %s (run with -update-documents, then read the diff)\n--- got ---\n%s", name, got)
			}
		})
	}
}

// The whole point of the phase-6-adjacent fix: a real capture's front matter
// becomes the metadata the item page and the prompt use.
func TestMarkdownFrontMatterBecomesMetadata(t *testing.T) {
	result := documentFixture(t, "gwern")
	cases := map[string]struct{ got, want string }{
		"title":    {result.Title, "First, Make Me Care"},
		"byline":   {result.Byline, "Gwern"},
		"language": {result.Language, ""},
	}
	for field, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", field, c.got, c.want)
		}
	}
	if !strings.HasPrefix(result.Excerpt, "Writing advice:") {
		t.Errorf("excerpt = %q, want the description", result.Excerpt)
	}
	// A quoted value keeps its colons and loses its quotes.
	if strings.Contains(result.Excerpt, `"`) {
		t.Errorf("excerpt kept its quotes: %q", result.Excerpt)
	}
}

// A relative thumbnail is resolved against the page it was fetched from, the
// same rule as the HTML path (issue #63).
func TestMarkdownCoverResolvesAgainstThePage(t *testing.T) {
	result := documentFixture(t, "gwern")
	want := "https://gwern.net/doc/ai/nn/diffusion/imagen/2026-02-07-thinkingface.jpg"
	if result.CoverURL != want {
		t.Errorf("CoverURL = %q, want %q", result.CoverURL, want)
	}
	if result.OG.Image != want {
		t.Errorf("OG.Image = %q, want the resolved cover %q", result.OG.Image, want)
	}
}

func TestMarkdownPublishedDate(t *testing.T) {
	result := documentFixture(t, "gwern")
	if result.Published == nil {
		t.Fatal("Published is nil; the front matter carries created: 2026-01-14")
	}
	if want := "2026-01-14"; result.Published.Format("2006-01-02") != want {
		t.Errorf("Published = %s, want %s", result.Published.Format("2006-01-02"), want)
	}
}

// The body is kept as it is: no Markdown stripping, and no front matter left in
// the text that is indexed and embedded.
func TestMarkdownBodyIsTheDocumentMinusTheFence(t *testing.T) {
	result := documentFixture(t, "gwern")
	if strings.Contains(result.Text, "thumbnail:") || strings.Contains(result.Text, "importance:") {
		t.Errorf("the front matter leaked into the body: %q", firstLine(result.Text))
	}
	if !strings.Contains(result.Text, "maritime empire") {
		t.Errorf("the body is missing: %q", firstLine(result.Text))
	}
	if !strings.Contains(result.Text, "](") {
		t.Errorf("Markdown syntax was stripped, which this deliberately does not do: %q", firstLine(result.Text))
	}
	if result.Markdown != result.Text {
		t.Error("Text and Markdown differ; for a document they are the same bytes")
	}
}

// CRLF, single quotes and a double-quoted value all read the same way.
func TestMarkdownHandlesCRLFAndQuoting(t *testing.T) {
	result := documentFixture(t, "crlf")
	if result.Title != "Quoted and CRLF" {
		t.Errorf("Title = %q", result.Title)
	}
	if result.Byline != "Someone" {
		t.Errorf("Byline = %q, want the single-quoted value", result.Byline)
	}
	if result.CoverURL != "https://cdn.example/cover.png" {
		t.Errorf("CoverURL = %q", result.CoverURL)
	}
	if strings.Contains(result.Text, "\r") {
		t.Error("a carriage return survived into the body")
	}
	if got := firstLine(result.Text); got != "First paragraph." {
		t.Errorf("body starts with %q", got)
	}
}

// An opening fence with no closing one is a horizontal rule, not metadata: the
// file may simply start with one.
func TestUnterminatedFenceIsBodyNotMetadata(t *testing.T) {
	result := documentFixture(t, "unterminated")
	if result.Title != "" {
		t.Errorf("Title = %q; nothing above an unclosed fence is metadata", result.Title)
	}
	if !strings.Contains(result.Text, "never closes it") {
		t.Errorf("the body was truncated: %q", result.Text)
	}
	if !strings.Contains(result.Text, "Title: this line looks like metadata") {
		t.Error("a metadata-looking line in the body was dropped")
	}
}

// Metadata with no body is not an extraction failure: the item keeps what it
// has, and the pipeline decides what to do with empty text.
func TestFrontMatterOnlyKeepsItsMetadata(t *testing.T) {
	result := documentFixture(t, "front-matter-only")
	if result.Title != "Notes with no body" {
		t.Errorf("Title = %q", result.Title)
	}
	if result.Text != "" {
		t.Errorf("Text = %q, want empty", result.Text)
	}
}

// A plain text file has no metadata to find and must not be parsed for it.
func TestPlainTextIsJustItsBody(t *testing.T) {
	result := documentFixture(t, "plain")
	if result.Title != "" || result.Excerpt != "" || result.CoverURL != "" {
		t.Errorf("a plain text file produced metadata: %+v", result)
	}
	if !strings.Contains(result.Text, "A plain text file, not Markdown.") {
		t.Errorf("Text = %q", result.Text)
	}
}

// The content-type predicates are the pipeline's whole decision, so they are
// asserted directly rather than through a fetch.
func TestContentTypePredicates(t *testing.T) {
	cases := []struct {
		contentType string
		html        bool
		document    bool
	}{
		{"text/html", true, false},
		{"text/html; charset=utf-8", true, false},
		{"APPLICATION/XHTML+XML", true, false},
		{"application/xml", true, false},
		{"text/xml", true, false},
		{"text/markdown", false, true},
		{"text/markdown; charset=utf-8", false, true},
		{"text/x-markdown", false, true},
		{"text/plain", false, true},
		{"text/plain; charset=iso-8859-1", false, true},
		{"application/pdf", false, false},
		{"image/jpeg", false, false},
		{"application/octet-stream", false, false},
		{"", false, false},
	}
	for _, c := range cases {
		if got := IsHTML(c.contentType); got != c.html {
			t.Errorf("IsHTML(%q) = %v, want %v", c.contentType, got, c.html)
		}
		if got := IsDocument(c.contentType); got != c.document {
			t.Errorf("IsDocument(%q) = %v, want %v", c.contentType, got, c.document)
		}
	}
}

// text/plain was accepted by the HTML reader before this change, which sent a
// text file into a parser that found no article in it. It has its own reader now.
func TestPlainTextIsNotHTML(t *testing.T) {
	if IsHTML("text/plain") {
		t.Error("IsHTML(\"text/plain\") is true; a text file has no HTML to read")
	}
	if !IsPlainText("text/plain; charset=utf-8") {
		t.Error("IsPlainText(\"text/plain; charset=utf-8\") is false")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
