package extract

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Documents are pages that are not HTML but are still worth reading: a Markdown
// file served by a CMS or a CDN's "markdown for agents" response, and plain
// text. They are readable as they are, so there is nothing to extract — what
// this file adds is the metadata a static site writes in front matter, and the
// decision to treat the bytes as text instead of refusing them.
//
// The shapes come from a real capture: gwern.net answers Harbor with
// text/markdown and a YAML front matter block, which is why the parser below
// exists at all (issue #73).

// Content-type predicates. They live next to the parsers rather than in the
// pipeline so the reader and the pipeline cannot disagree about what "readable"
// means: a scored capture uses the same rule the enricher does.
func mediaTypeOf(contentType string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
}

// IsHTML reports whether a content type is one the HTML extractor reads.
// text/plain is deliberately not here: it used to be, and a plain-text body fed
// to an HTML parser produces an empty article and a permanent failure. It has
// its own reader now.
func IsHTML(contentType string) bool {
	switch mediaTypeOf(contentType) {
	case "text/html", "application/xhtml+xml", "application/xml", "text/xml":
		return true
	}
	return false
}

// IsMarkdown reports whether a content type is Markdown.
func IsMarkdown(contentType string) bool {
	switch mediaTypeOf(contentType) {
	case "text/markdown", "text/x-markdown":
		return true
	}
	return false
}

// IsPlainText reports whether a content type is plain text.
func IsPlainText(contentType string) bool {
	switch mediaTypeOf(contentType) {
	case "text/plain":
		return true
	}
	return false
}

// IsDocument reports whether a type is readable but not HTML.
func IsDocument(contentType string) bool {
	return IsMarkdown(contentType) || IsPlainText(contentType)
}

// Document reads a Markdown or plain-text body. base resolves a relative
// front-matter image and is the URL the body was fetched from; nil is allowed.
//
// The body is kept as it is, in both Text and Markdown. Stripping Markdown
// syntax would be a second parser in the one place that must not lose text, and
// it buys little: FTS5's tokenizer already drops punctuation, and a chat model
// reads Markdown natively. Content_text and content_md are the same bytes for
// these pages, and that is not a bug.
func Document(r io.Reader, contentType string, base *url.URL) (*Result, error) {
	if r == nil {
		return nil, errors.New("extract: no input")
	}
	if base == nil {
		base = &url.URL{}
	}

	// The caller has already capped the body, but this function is also a target
	// of a fuzz test and of the scored recorder: bound it here too so the caps
	// hold whoever calls. One byte past the cap is enough to know it was cut.
	raw, err := io.ReadAll(io.LimitReader(r, int64(MaxTextBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("extract: reading the document: %w", err)
	}
	body := normaliseText(string(raw))

	result := &Result{}
	if IsMarkdown(contentType) {
		meta, rest := frontMatter(body)
		body = rest
		result.Title = truncate(meta["title"], MaxTitleBytes)
		result.Byline = truncate(firstNonEmpty(meta["author"], meta["byline"]), MaxTitleBytes)
		result.SiteName = truncate(firstNonEmpty(meta["site"], meta["site-name"], meta["site_name"]), MaxTitleBytes)
		result.Language = firstNonEmpty(meta["language"], meta["lang"])
		result.Excerpt = truncate(firstNonEmpty(meta["description"], meta["summary"], meta["excerpt"]), MaxExcerptBytes)
		if image := firstNonEmpty(meta["thumbnail"], meta["image"], meta["cover"]); image != "" {
			// Resolved here, against the URL the body actually came from, for the
			// same reason as the HTML path (issue #63): a relative image resolved
			// later against a redirected URL never fetches.
			result.CoverURL = resolve(base, image)
		}
		if parsed, ok := parseTime(firstNonEmpty(meta["created"], meta["date"], meta["published"], meta["modified"])); ok {
			result.Published = &parsed
		}
		result.OG = OpenGraph{
			Title:         result.Title,
			Description:   result.Excerpt,
			SiteName:      result.SiteName,
			Image:         result.CoverURL,
			Author:        result.Byline,
			URL:           base.String(),
			PublishedTime: firstNonEmpty(meta["created"], meta["date"], meta["published"]),
			Locale:        result.Language,
		}
	}

	result.Text = truncate(body, MaxTextBytes)
	result.Markdown = truncate(body, MaxMarkdownBytes)
	return result, nil
}

// frontMatter splits a leading YAML block from the body and returns it as a flat
// map, plus the rest of the document.
//
// It understands the shape a static site generator writes — "key: value", one
// per line, optionally quoted — and nothing else: no nesting, no lists, no
// anchors. An unrecognised line is skipped rather than rejected, because the
// body is what matters and losing the document over a metadata line would be the
// worse failure. A block that never closes is not front matter at all: the file
// may simply start with a horizontal rule, so the whole body is kept.
func frontMatter(body string) (map[string]string, string) {
	meta := map[string]string{}
	body = strings.TrimPrefix(body, "\ufeff")
	if !strings.HasPrefix(body, "---") {
		return meta, body
	}
	lines := strings.Split(body, "\n")
	// The opening delimiter must be a line of its own: "---" or "--- something"
	// is a heading or a rule, not a fence.
	if strings.TrimRight(lines[0], " \t") != "---" {
		return meta, body
	}
	close := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			close = i
			break
		}
	}
	if close < 0 {
		return meta, body
	}
	for _, line := range lines[1:close] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || strings.HasPrefix(key, "#") {
			continue
		}
		meta[key] = unquote(strings.TrimSpace(value))
	}
	return meta, strings.TrimSpace(strings.Join(lines[close+1:], "\n"))
}

// unquote removes one layer of matching quotes. It does not decode escapes: a
// front-matter value with an escaped quote in it keeps the backslash, which
// reads correctly and is not worth a second parser.
func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	first, last := value[0], value[len(value)-1]
	if (first == '\'' || first == '"') && last == first {
		return value[1 : len(value)-1]
	}
	return value
}
