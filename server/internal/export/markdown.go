// The Markdown writer: one file per item, readable in any editor, with no part
// of the library's meaning left behind in a proprietary structure (plans/12a).
package export

import (
	"encoding/json"
	"fmt"
	"strings"
)

// markdownFiles renders the library as markdown/<seq>-<slug>.md. Rendering from
// the same itemEntry view the JSON uses keeps the two formats from disagreeing
// about what an item is.
func markdownFiles(entries []itemEntry) map[string][]byte {
	files := make(map[string][]byte, len(entries))
	for i, entry := range entries {
		name := fmt.Sprintf("markdown/%04d-%s.md", i+1, slug(entry.Title))
		files[name] = []byte(markdownItem(entry))
	}
	return files
}

// markdownItem is front-matter (valid YAML, written as JSON scalars so there is
// one escaping rule), then the item as prose.
func markdownItem(entry itemEntry) string {
	var b strings.Builder
	b.WriteString("---\n")
	field(&b, "title", entry.Title)
	if entry.URL != nil {
		field(&b, "url", *entry.URL)
	}
	field(&b, "url_original", entry.URLOriginal)
	field(&b, "kind", entry.Kind)
	field(&b, "kind_source", entry.KindSource)
	field(&b, "source", entry.Source)
	field(&b, "created_at", entry.CreatedAt)
	field(&b, "saved_at", entry.SavedAt)
	field(&b, "updated_at", entry.UpdatedAt)
	if entry.DeletedAt != nil {
		field(&b, "deleted_at", *entry.DeletedAt)
	}
	b.WriteString("tags: " + jsonScalar(entry.Tags) + "\n")
	b.WriteString("spaces: " + jsonScalar(entry.Spaces) + "\n")
	b.WriteString("---\n\n")

	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = "Untitled item"
	}
	b.WriteString("# " + title + "\n\n")

	if summary := strings.TrimSpace(entry.Summary); summary != "" {
		b.WriteString("> " + strings.ReplaceAll(summary, "\n", "\n> ") + "\n\n")
	}

	if note := strings.TrimSpace(entry.Note); note != "" {
		b.WriteString("## Note\n\n" + note + "\n\n")
	}

	// The reader's Markdown is the richer copy; extracted plain text is the
	// fallback when a page never produced one.
	if body := strings.TrimSpace(firstNonEmpty(entry.ContentMD, entry.ContentText)); body != "" {
		b.WriteString("## Content\n\n" + body + "\n\n")
	}

	if transcript := entry.Transcript; transcript != nil && strings.TrimSpace(transcript.Text) != "" {
		b.WriteString("## Transcript\n\n" + transcript.Text + "\n\n")
	}

	if len(entry.Highlights) > 0 {
		b.WriteString("## Highlights\n\n")
		for _, highlight := range entry.Highlights {
			b.WriteString("> " + strings.ReplaceAll(highlight.Quote, "\n", "\n> ") + "\n\n")
			if note := strings.TrimSpace(highlight.Note); note != "" {
				b.WriteString(note + "\n\n")
			}
		}
	}

	if len(entry.Media) > 0 {
		b.WriteString("## Media\n\n")
		for _, media := range entry.Media {
			// Relative to the markdown directory, so the export opens as a tree.
			path := "../media/" + media.SHA256
			if strings.HasPrefix(media.ContentType, "image/") {
				b.WriteString(fmt.Sprintf("![%s](%s)\n\n", firstNonEmpty(media.Filename, media.SHA256), path))
				continue
			}
			b.WriteString(fmt.Sprintf("- [%s](%s) (%s)\n", firstNonEmpty(media.Filename, media.SHA256), path, media.ContentType))
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func field(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	b.WriteString(name + ": " + jsonScalar(value) + "\n")
}

// jsonScalar is a JSON encoding, which is also a YAML scalar: one escaping rule
// for quotes, colons, newlines and unicode, and no hand-rolled YAML.
func jsonScalar(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// slug is the human half of a filename: lowercase ASCII words joined by dashes.
// The numeric prefix carries uniqueness, so two items called "Notes" never
// collide and no slug ever has to be invented twice.
func slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case dash:
			// collapse a run of separators into one dash
		default:
			if b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 60 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "untitled"
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
