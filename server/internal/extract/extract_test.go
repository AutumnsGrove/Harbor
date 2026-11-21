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
	"unicode/utf8"
)

// plans/03 tests 5 and 19 — extraction is a pure function of bytes, so the
// corpus is HTML on disk and the expected output is reviewed as a diff. Run
// `go test ./server/internal/extract -run TestExtractFixtures -update` after an
// intentional change and read the diff before committing it.

var update = flag.Bool("update", false, "rewrite the golden files")

// baseURLs are the fixtures' final addresses. They matter: a relative cover
// only resolves correctly against the page it came from.
var baseURLs = map[string]string{
	"article":      "https://fieldnotes.example/quiet-web",
	"recipe":       "https://kitchen.example/pancakes",
	"product":      "https://shop.example/p/notebook",
	"place":        "https://places.example/bakery",
	"video":        "https://video.example/watch",
	"malformed":    "https://malformed.example/page",
	"empty":        "https://empty.example/",
	"js-only":      "https://app.example/shell",
	"non-latin":    "https://fieldnotes.example/ja",
	"jsonld-graph": "https://kitchen.example/oats",
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob("testdata/pages/*.html")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixture pages found")
	}
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, strings.TrimSuffix(filepath.Base(path), ".html"))
	}
	return names
}

func extractFixture(t *testing.T, name string) *Result {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "pages", name+".html"))
	if err != nil {
		t.Fatalf("opening fixture %s: %v", name, err)
	}
	defer file.Close()
	base := parseBase(t, name)
	result, err := HTML(file, base)
	if err != nil {
		t.Fatalf("extracting %s: %v", name, err)
	}
	return result
}

func parseBase(t *testing.T, name string) *url.URL {
	t.Helper()
	raw, ok := baseURLs[name]
	if !ok {
		t.Fatalf("fixture %s has no base URL; add one to baseURLs", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("base URL for %s: %v", name, err)
	}
	return parsed
}

func TestExtractFixtures(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			result := extractFixture(t, name)
			got, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				t.Fatalf("marshalling the result: %v", err)
			}
			got = append(got, '\n')

			path := filepath.Join("testdata", "golden", name+".json")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("writing %s: %v", path, err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s (run with -update to create it): %v", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("golden mismatch for %s (run with -update, then read the diff)\n--- got ---\n%s", name, got)
			}
		})
	}
}

func TestRelativeCoverIsResolvedAgainstThePage(t *testing.T) {
	result := extractFixture(t, "article")
	if want := "https://fieldnotes.example/images/quiet-web.png"; result.CoverURL != want {
		t.Errorf("CoverURL = %q, want %q", result.CoverURL, want)
	}
}

// A broken page is not a failed extraction: the title survives, JSON-LD does
// not, and the body is still text (plans/03 test 19).
func TestMalformedPageStillExtracts(t *testing.T) {
	result := extractFixture(t, "malformed")
	if result.Title != "Broken but readable" {
		t.Errorf("Title = %q, want the title tag", result.Title)
	}
	if len(result.Details) != 0 {
		t.Errorf("Details = %v, want {} for invalid JSON-LD", result.Details)
	}
	if !strings.Contains(result.Text, "Unclosed tags are normal") {
		t.Errorf("Text = %q, want the broken body's text", result.Text)
	}
}

func TestJSONLDGraphPrefersTheRecipe(t *testing.T) {
	result := extractFixture(t, "jsonld-graph")
	if got := result.Details["type"]; got != "Recipe" {
		t.Fatalf("details type = %v, want Recipe", got)
	}
	if got := result.Details["name"]; got != "Overnight oats" {
		t.Errorf("name = %v", got)
	}
	if got, ok := result.Details["instructions"].([]string); !ok || len(got) != 1 {
		t.Errorf("instructions = %v, want one step", result.Details["instructions"])
	}
}

// A schema.org subtype shares its parent's layout; a bakery is a place.
func TestJSONLDSubtypeFoldsIntoItsLayout(t *testing.T) {
	result := extractFixture(t, "place")
	if got := result.Details["type"]; got != "Place" {
		t.Fatalf("details type = %v, want Place for a Bakery", got)
	}
	address, ok := result.Details["address"].(map[string]any)
	if !ok || address["addressLocality"] != "Ely" {
		t.Errorf("address = %v, want the bakery's address", result.Details["address"])
	}
}

func TestEmptyPageHasNoBodyAndIsNotAnError(t *testing.T) {
	result := extractFixture(t, "empty")
	if result.Title != "Nothing here" {
		t.Errorf("Title = %q", result.Title)
	}
	if result.Text != "" || result.Markdown != "" {
		t.Errorf("Text/Markdown = %q/%q, want empty", result.Text, result.Markdown)
	}
	if len(result.Details) != 0 {
		t.Errorf("Details = %v, want {}", result.Details)
	}
}

// A JS-only shell gives readability nothing, but OG still names the page; the
// item can exist and say what it is.
func TestJSPageKeepsItsMetadata(t *testing.T) {
	result := extractFixture(t, "js-only")
	if result.Title != "A page that needs JavaScript" {
		t.Errorf("Title = %q, want the OG title", result.Title)
	}
	if result.SiteName != "App" {
		t.Errorf("SiteName = %q", result.SiteName)
	}
	if result.Excerpt != "The content is rendered in the browser." {
		t.Errorf("Excerpt = %q", result.Excerpt)
	}
}

func TestNonLatinTextSurvives(t *testing.T) {
	result := extractFixture(t, "non-latin")
	if !utf8.ValidString(result.Text) || !utf8.ValidString(result.Markdown) {
		t.Fatal("extraction produced invalid UTF-8")
	}
	if !strings.Contains(result.Title, "静かなウェブ") {
		t.Errorf("Title = %q, want the Japanese title", result.Title)
	}
	if !strings.Contains(result.Text, "インターネット") {
		t.Errorf("Text lost the Japanese body: %q", result.Text)
	}
}

func TestOutputIsBounded(t *testing.T) {
	var page strings.Builder
	page.WriteString("<html><head><title>Huge</title></head><body><article><p>")
	page.WriteString(strings.Repeat("word ", (3<<20)/5))
	page.WriteString("</p></article></body></html>")

	result, err := HTML(strings.NewReader(page.String()), nil)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if len(result.Text) > MaxTextBytes {
		t.Errorf("Text is %d bytes, above the %d cap", len(result.Text), MaxTextBytes)
	}
	if len(result.Markdown) > MaxMarkdownBytes {
		t.Errorf("Markdown is %d bytes, above the %d cap", len(result.Markdown), MaxMarkdownBytes)
	}
	if !utf8.ValidString(result.Text) {
		t.Error("the capped text is not valid UTF-8")
	}
}

func TestNilBaseIsAllowed(t *testing.T) {
	result, err := HTML(strings.NewReader(`<html><body><p>no base</p></body></html>`), nil)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if !strings.Contains(result.Text, "no base") {
		t.Errorf("Text = %q", result.Text)
	}
}

// Fuzzing found this: a lone 0xE3 byte survives the HTML parser and must not
// reach the database as invalid UTF-8.
func TestInvalidUTF8IsReplaced(t *testing.T) {
	result, err := HTML(strings.NewReader("<html><body><p>before \xe3 after</p></body></html>"), nil)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if !utf8.ValidString(result.Text) || !utf8.ValidString(result.Markdown) {
		t.Fatalf("invalid UTF-8 in output: text %q, markdown %q", result.Text, result.Markdown)
	}
	if !strings.Contains(result.Text, "before") || !strings.Contains(result.Text, "after") {
		t.Errorf("text = %q, want the valid parts kept", result.Text)
	}
}
