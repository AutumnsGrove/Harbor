// Package extract turns one fetched page into the fields the library stores:
// title, readable text, Markdown, Open Graph metadata and the schema.org
// details behind the per-kind pages (design.md §6 step 2).
//
// It never fetches anything and never calls a model. That keeps it a pure
// function of bytes, which is what makes the golden corpus and the fuzz target
// meaningful.
package extract

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-shiori/go-readability"
	"golang.org/x/net/html"
)

// Output caps. A page is already capped at 5 MB by fetch; these keep the
// extractor's own output bounded too, so a pathological document cannot write
// 5 MB of text into SQLite and the prompt (plans/03 test 5).
const (
	MaxTextBytes     = 2 << 20
	MaxMarkdownBytes = 2 << 20
	MaxExcerptBytes  = 2_000
	MaxTitleBytes    = 512
)

// Result is one extracted page. Details is the curated JSON-LD object; the
// caller marshals it into items.details_json.
type Result struct {
	Title     string
	Byline    string
	SiteName  string
	Language  string
	Excerpt   string
	Published *time.Time
	CoverURL  string

	Text     string
	Markdown string

	// PDF, when set, describes a PDF: how many pages it has, how much text it
	// gave up, and whether it carried a text layer at all. It is nil for every
	// other shape, so nothing else in the pipeline has to know about it.
	PDF *PDFInfo

	OG      OpenGraph
	Details map[string]any
}

// HTML parses one document. base resolves relative links and is the page's
// final URL; nil is allowed (a test or a page with no URL).
//
// Readability failure is not an extraction failure: a page can have usable
// title and metadata with no readable article, and the item still deserves
// them. Only a document the HTML parser cannot read at all is an error.
func HTML(r io.Reader, base *url.URL) (*Result, error) {
	if r == nil {
		return nil, errors.New("extract: no input")
	}
	if base == nil {
		base = &url.URL{}
	}

	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("extract: parsing HTML: %w", err)
	}
	sel := newSelection(doc)

	// Metadata first: readability mutates the document by removing nodes.
	og := readOpenGraph(sel)
	lang := strings.TrimSpace(sel.Find("html").First().AttrOr("lang", ""))
	titleTag := strings.TrimSpace(sel.Find("title").First().Text())
	bodyText := strings.TrimSpace(sel.Find("body").First().Text())
	details := readJSONLD(sel)

	article, articleErr := readability.FromDocument(doc, base)
	if articleErr != nil {
		// Keep whatever metadata exists; the body falls back to the page text
		// only when there is nothing readable, and is marked by being short.
		article = readability.Article{}
	}

	result := &Result{
		Title:    truncate(firstNonEmpty(article.Title, og.Title, titleTag, og.SiteName), MaxTitleBytes),
		Byline:   truncate(firstNonEmpty(article.Byline, og.Author), MaxTitleBytes),
		SiteName: truncate(firstNonEmpty(og.SiteName, article.SiteName), MaxTitleBytes),
		Language: firstNonEmpty(article.Language, lang),
		Excerpt:  truncate(firstNonEmpty(article.Excerpt, og.Description), MaxExcerptBytes),
		CoverURL: resolve(base, firstNonEmpty(og.Image, article.Image)),
		OG:       og,
		Details:  details,
	}
	if result.Title == "" && base.Host != "" {
		result.Title = base.Host
	}
	if article.PublishedTime != nil {
		published := article.PublishedTime.UTC()
		result.Published = &published
	} else if parsed, ok := parseTime(og.PublishedTime); ok {
		result.Published = &parsed
	} else if detailDate := text(details["published"]); detailDate != "" {
		// JSON-LD often carries the date when no meta tag does.
		if parsed, ok := parseTime(detailDate); ok {
			result.Published = &parsed
		}
	}

	text := article.TextContent
	if strings.TrimSpace(text) == "" {
		text = bodyText
	}
	result.Text = truncate(normaliseText(text), MaxTextBytes)

	if article.Node != nil {
		markdown, err := toMarkdown(article.Node)
		if err == nil {
			result.Markdown = truncate(markdown, MaxMarkdownBytes)
		}
	}

	return result, nil
}

// newSelection wraps a parsed document for metadata queries.
func newSelection(doc *html.Node) *goquery.Document {
	return goquery.NewDocumentFromNode(doc)
}

// firstNonEmpty returns the first trimmed non-empty value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// resolve turns a page-relative URL into an absolute one, leaving anything it
// cannot parse untouched rather than dropping it.
func resolve(base *url.URL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if base == nil {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return base.ResolveReference(parsed).String()
}

// normaliseText makes stored text stable: one line ending, no NULs, trimmed.
// It does not re-wrap paragraphs — the Markdown carries the structure.
func normaliseText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\x00", "")
	return strings.TrimSpace(text)
}

// validUTF8 replaces malformed bytes. An HTML parser is allowed to pass a
// truncated multi-byte sequence straight through (fuzzing found exactly that),
// and everything stored must be valid text.
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// truncate cuts at a UTF-8 boundary, so output is always valid UTF-8.
func truncate(s string, max int) string {
	s = validUTF8(s)
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// parseTime accepts the shapes a page actually uses for a publication date.
func parseTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, time.RFC1123Z, time.RFC1123, "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}
