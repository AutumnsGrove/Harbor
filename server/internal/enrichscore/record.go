// Recording a scored set: a list of ids and URLs is fetched through Harbor's own
// fetch and extract, and each page becomes one committed case file.
package enrichscore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	"github.com/AutumnsGrove/Harbor/server/internal/extract"
)

// recordTextLimit bounds what a capture keeps. The pipeline sends at most
// 8,000 characters to any model (enrich's chat cap), so a longer capture could
// never change a score, and a fixture nobody can read is a fixture nobody
// corrects.
const recordTextLimit = 20_000

// Source is one line of a source list: the id the file will be called, and the
// page to capture.
type Source struct {
	ID  string
	URL string
}

// RecordFailure is one page that could not be captured. The rest of the set is
// still written: one dead link must not throw away a forty-page run.
type RecordFailure struct {
	ID  string
	Err error
}

// idPattern is what can become a filename. Anything that could escape the
// directory is refused here rather than at the filesystem.
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseSources reads `id url` lines, `#` comments and blank lines.
func ParseSources(r io.Reader) ([]Source, error) {
	scanner := bufio.NewScanner(r)
	seen := map[string]bool{}
	var sources []Source
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return nil, fmt.Errorf("enrichscore: line %d: want `id url`, got %q", line, text)
		}
		source := Source{ID: fields[0], URL: fields[1]}
		if !idPattern.MatchString(source.ID) {
			return nil, fmt.Errorf("enrichscore: line %d: %q is not a lowercase filename", line, source.ID)
		}
		if seen[source.ID] {
			return nil, fmt.Errorf("enrichscore: line %d: %q appears twice", line, source.ID)
		}
		parsed, err := url.Parse(source.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("enrichscore: line %d: %q is not an http(s) URL", line, source.URL)
		}
		seen[source.ID] = true
		sources = append(sources, source)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("enrichscore: reading the source list: %w", err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("enrichscore: the source list is empty")
	}
	return sources, nil
}

// Record captures every source and writes one case file per page. It leaves
// existing labels alone and gives every captured id an entry in labels.json, so
// labelling a set is one file of forty lines rather than forty files.
func Record(ctx context.Context, fetcher enrich.Fetcher, dir string, sources []Source, now func() time.Time) ([]RecordFailure, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("enrichscore: creating %s: %w", dir, err)
	}
	captured := now().UTC().Format("2006-01-02")
	var failures []RecordFailure
	written := make([]string, 0, len(sources))
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return failures, err
		}
		page, err := capture(ctx, fetcher, source)
		if err != nil {
			failures = append(failures, RecordFailure{ID: source.ID, Err: err})
			continue
		}
		if err := writeCase(dir, Case{
			ID:       source.ID,
			URL:      source.URL,
			Captured: captured,
			Title:    page.Title,
			SiteName: page.SiteName,
			Excerpt:  page.Excerpt,
			Text:     limit(page.Text, recordTextLimit),
		}); err != nil {
			return failures, err
		}
		written = append(written, source.ID)
	}

	labels, err := loadLabels(dir)
	if err != nil {
		return failures, err
	}
	for _, id := range written {
		if _, ok := labels[id]; !ok {
			labels[id] = label{}
		}
	}
	if err := writeLabels(dir, labels); err != nil {
		return failures, err
	}
	return failures, nil
}

// writeLabels renders the one file the owner edits: an id per line, so a set
// can be labelled without opening a file per save. Blank kinds are unlabelled.
func writeLabels(dir string, labels map[string]label) error {
	ids := make([]string, 0, len(labels))
	for id := range labels {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var b strings.Builder
	b.WriteString("{\n")
	for i, id := range ids {
		tags := "[]"
		if len(labels[id].Tags) > 0 {
			raw, err := json.Marshal(labels[id].Tags)
			if err != nil {
				return fmt.Errorf("enrichscore: encoding tags for %s: %w", id, err)
			}
			tags = string(raw)
		}
		fmt.Fprintf(&b, "  %q: {\"kind\": %q, \"tags\": %s}", id, string(labels[id].Kind), tags)
		if i < len(ids)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")

	path := filepath.Join(dir, labelsFile)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("enrichscore: writing %s: %w", path, err)
	}
	return nil
}

// capture is fetch plus extract, closed together so a temp file never outlives
// its page.
func capture(ctx context.Context, fetcher enrich.Fetcher, source Source) (*extract.Result, error) {
	fetched, err := fetcher.Fetch(ctx, source.URL)
	if err != nil {
		return nil, err
	}
	defer fetched.Close()

	base := fetched.URL
	if base == nil {
		base = &url.URL{}
	}
	result, err := extract.HTML(fetched.Body, base)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(result.Text) == "" {
		return nil, fmt.Errorf("extracted no page text")
	}
	return result, nil
}

// limit cuts at a UTF-8 boundary; a fixture is not allowed to hold invalid
// bytes, or every diff and every editor mangles it.
func limit(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
