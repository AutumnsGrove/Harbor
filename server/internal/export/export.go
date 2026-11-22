// Package export writes the library out in forms that outlive the application
// (plans/12a): library.json is the restorable copy, markdown and bookmarks.html
// are the readable ones, and manifest.json verifies the lot.
//
// Nothing here touches the network. An export that needs the Worker, R2 or a
// model is not an export, and phase 12's first invariant is that it works from
// any shipped version with none of them.
package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// ExportVersion is the contract for the files this package writes. Adding a
// field is fine; renaming one is a breaking change that bumps this number,
// because a restore reads it.
const ExportVersion = 1

// Reader is the part of the store an export needs. It is an interface so the
// golden test can hand over a library instead of a database.
type Reader interface {
	ExportLibrary(ctx context.Context) (model.Library, error)
}

// Options is one export run.
type Options struct {
	Dir           string // the directory to create; it must be new or empty
	IncludeMedia  bool   // copy media blobs into media/<sha256>
	MediaDir      string // where Media.LocalPath files live (data/media)
	AppVersion    string
	SchemaVersion int
	Now           func() time.Time
}

// File is one written file, as the manifest records it.
type File struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Result is what an export wrote.
type Result struct {
	Dir   string
	Files []File
}

// Has reports whether the export wrote path (slash-separated).
func (r Result) Has(path string) bool {
	for _, file := range r.Files {
		if file.Path == path {
			return true
		}
	}
	return false
}

// JSON writes library.json and manifest.json. It refuses a directory that has
// anything in it: an export is a new copy, never a merge into an old one.
func JSON(ctx context.Context, reader Reader, opts Options) (Result, error) {
	library, err := reader.ExportLibrary(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := prepareDir(opts.Dir); err != nil {
		return Result{}, err
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}
	at := now()
	exportedAt := at.UTC().Format(time.RFC3339)
	counts := countOf(library)

	doc := libraryFile{
		ExportVersion: ExportVersion,
		AppVersion:    opts.AppVersion,
		SchemaVersion: opts.SchemaVersion,
		ExportedAt:    exportedAt,
		Counts:        counts,
		Spaces:        spaceEntries(library),
		Items:         itemEntries(library),
	}
	raw, err := marshal(doc)
	if err != nil {
		return Result{}, err
	}
	result := Result{Dir: opts.Dir}
	if err := result.write(opts.Dir, "library.json", raw); err != nil {
		return Result{}, err
	}

	if opts.IncludeMedia {
		media, err := writeMedia(opts, library.Media)
		if err != nil {
			return Result{}, err
		}
		result.Files = append(result.Files, media...)
	}

	// The manifest describes everything else, never itself: a file cannot
	// contain its own hash.
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	manifest, err := marshal(manifestFile{
		ExportVersion: ExportVersion,
		AppVersion:    opts.AppVersion,
		SchemaVersion: opts.SchemaVersion,
		ExportedAt:    exportedAt,
		Counts:        counts,
		Files:         result.Files,
	})
	if err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(opts.Dir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	// It is in the result for a caller that wants to report the whole tree, but
	// not in the manifest's own file list.
	result.Files = append(result.Files, File{Path: "manifest.json", Bytes: int64(len(manifest))})
	sort.Slice(result.Files, func(i, j int) bool { return result.Files[i].Path < result.Files[j].Path })
	return result, nil
}

func (r *Result) write(dir, name string, body []byte) error {
	file := filepath.Join(dir, name)
	if err := writeFile(file, body); err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	r.Files = append(r.Files, File{Path: name, Bytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:])})
	return nil
}

// writeMedia copies each blob to media/<sha256>, refusing to continue when one
// is missing: a manifest that lists a hash with no bytes is a broken promise.
func writeMedia(opts Options, media []model.Media) ([]File, error) {
	dir := filepath.Join(opts.Dir, "media")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("export: creating %s: %w", dir, err)
	}
	var files []File
	seen := map[string]bool{}
	for _, entry := range media {
		if seen[entry.SHA256] {
			continue
		}
		seen[entry.SHA256] = true
		source := filepath.Join(opts.MediaDir, filepath.FromSlash(entry.LocalPath))
		written, err := copyFile(source, filepath.Join(dir, entry.SHA256))
		if err != nil {
			return nil, fmt.Errorf("export: media %s for item %s: %w", entry.SHA256, entry.ItemID, err)
		}
		written.Path = path.Join("media", entry.SHA256)
		files = append(files, written)
	}
	return files, nil
}

func copyFile(source, dest string) (File, error) {
	in, err := os.Open(source)
	if err != nil {
		return File{}, err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return File{}, err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, hash), in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return File{}, err
	}
	return File{Bytes: written, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func writeFile(dest string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("export: creating %s: %w", filepath.Dir(dest), err)
	}
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		return fmt.Errorf("export: writing %s: %w", dest, err)
	}
	return nil
}

// prepareDir makes an export directory: a missing one is created, an existing
// one must be empty. Exporting on top of yesterday's copy would leave both
// versions in one tree, and the manifest would describe neither.
func prepareDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("export: no output directory")
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return os.MkdirAll(dir, 0o755)
	case err != nil:
		return fmt.Errorf("export: reading %s: %w", dir, err)
	case len(entries) > 0:
		return fmt.Errorf("export: %s already has %d entries; export into a new directory", dir, len(entries))
	}
	return nil
}

func marshal(doc any) ([]byte, error) {
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("export: encoding: %w", err)
	}
	return append(raw, '\n'), nil
}

// --- the file shapes --------------------------------------------------------

// These structs are the contract. Field names are part of it.
type libraryFile struct {
	ExportVersion int          `json:"export_version"`
	AppVersion    string       `json:"app_version"`
	SchemaVersion int          `json:"schema_version"`
	ExportedAt    string       `json:"exported_at"`
	Counts        counts       `json:"counts"`
	Spaces        []spaceEntry `json:"spaces"`
	Items         []itemEntry  `json:"items"`
}

type manifestFile struct {
	ExportVersion int    `json:"export_version"`
	AppVersion    string `json:"app_version"`
	SchemaVersion int    `json:"schema_version"`
	ExportedAt    string `json:"exported_at"`
	Counts        counts `json:"counts"`
	Files         []File `json:"files"`
}

type counts struct {
	Items      int `json:"items"`
	Tags       int `json:"tags"`
	Spaces     int `json:"spaces"`
	Highlights int `json:"highlights"`
	Media      int `json:"media"`
}

type spaceEntry struct {
	ID        string          `json:"id"`
	Slug      string          `json:"slug,omitempty"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	Rule      json.RawMessage `json:"rule"`
	CreatedAt string          `json:"created_at"`
}

type itemEntry struct {
	ID          string  `json:"id"`
	URL         *string `json:"url"`
	URLOriginal string  `json:"url_original"`
	Title       string  `json:"title"`
	Note        string  `json:"note"`
	Kind        string  `json:"kind"`
	KindSource  string  `json:"kind_source"`
	Summary     string  `json:"summary"`
	Source      string  `json:"source"`
	ImportRef   string  `json:"import_ref"`
	LinkStatus  string  `json:"link_status"`
	CreatedAt   string  `json:"created_at"`
	SavedAt     string  `json:"saved_at"`
	UpdatedAt   string  `json:"updated_at"`
	DeletedAt   *string `json:"deleted_at,omitempty"`

	Tags   []string `json:"tags"`
	Spaces []string `json:"spaces"`

	Details json.RawMessage `json:"details"`
	OG      json.RawMessage `json:"og"`

	ContentText string `json:"content_text"`
	ContentMD   string `json:"content_md"`

	Transcript *transcriptEntry `json:"transcript,omitempty"`
	Highlights []highlightEntry `json:"highlights"`
	Media      []mediaEntry     `json:"media"`
}

type transcriptEntry struct {
	Text  string `json:"text"`
	Lang  string `json:"lang"`
	State string `json:"state"`
}

type highlightEntry struct {
	ID        string          `json:"id"`
	Quote     string          `json:"quote"`
	Note      string          `json:"note"`
	Color     string          `json:"color"`
	Anchor    json.RawMessage `json:"anchor"`
	Detached  bool            `json:"detached"`
	CreatedAt string          `json:"created_at"`
}

type mediaEntry struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	Width       *int   `json:"width,omitempty"`
	Height      *int   `json:"height,omitempty"`
	Path        string `json:"path"`
}

func countOf(library model.Library) counts {
	tags := map[string]bool{}
	for _, names := range library.Tags {
		for _, name := range names {
			tags[name] = true
		}
	}
	return counts{
		Items:      len(library.Items),
		Tags:       len(tags),
		Spaces:     len(library.Spaces),
		Highlights: len(library.Highlights),
		Media:      len(library.Media),
	}
}

func spaceEntries(library model.Library) []spaceEntry {
	entries := make([]spaceEntry, 0, len(library.Spaces))
	for _, space := range library.Spaces {
		entry := spaceEntry{
			ID:        space.ID,
			Name:      space.Name,
			Kind:      string(space.Kind),
			Rule:      rawOrEmpty(space.RuleJSON),
			CreatedAt: stamp(space.CreatedAt),
		}
		if space.Slug != nil {
			entry.Slug = *space.Slug
		}
		entries = append(entries, entry)
	}
	return entries
}

// itemEntries nests each item's tags, Spaces, highlights and media, because
// library.json is meant to be read as one file per item, not joined by hand.
func itemEntries(library model.Library) []itemEntry {
	spaceNames := map[string][]string{}
	for _, space := range library.Spaces {
		for _, itemID := range library.SpaceItems[space.ID] {
			spaceNames[itemID] = append(spaceNames[itemID], space.Name)
		}
	}
	highlights := map[string][]highlightEntry{}
	for _, highlight := range library.Highlights {
		highlights[highlight.ItemID] = append(highlights[highlight.ItemID], highlightEntry{
			ID:        highlight.ID,
			Quote:     highlight.Quote,
			Note:      highlight.Note,
			Color:     string(highlight.Color),
			Anchor:    rawOrEmpty(highlight.AnchorJSON),
			Detached:  highlight.Detached,
			CreatedAt: stamp(highlight.CreatedAt),
		})
	}
	media := map[string][]mediaEntry{}
	for _, entry := range library.Media {
		media[entry.ItemID] = append(media[entry.ItemID], mediaEntry{
			ID:          entry.ID,
			Kind:        string(entry.Kind),
			Filename:    entry.Filename,
			ContentType: entry.ContentType,
			Bytes:       entry.Bytes,
			SHA256:      entry.SHA256,
			Width:       entry.Width,
			Height:      entry.Height,
			Path:        entry.LocalPath,
		})
	}

	entries := make([]itemEntry, 0, len(library.Items))
	for _, item := range library.Items {
		entry := itemEntry{
			ID:          item.ID,
			URL:         item.URL,
			URLOriginal: item.URLOriginal,
			Title:       item.Title,
			Note:        item.Note,
			Kind:        string(item.Kind),
			KindSource:  string(item.KindSource),
			Summary:     item.Summary,
			Source:      string(item.Source),
			ImportRef:   item.ImportRef,
			LinkStatus:  item.LinkStatus,
			CreatedAt:   stamp(item.CreatedAt),
			SavedAt:     stamp(item.SavedAt),
			UpdatedAt:   stamp(item.UpdatedAt),
			Tags:        library.Tags[item.ID],
			Spaces:      spaceNames[item.ID],
			Details:     rawOrEmpty(item.DetailsJSON),
			OG:          rawOrEmpty(item.OGJSON),
			ContentText: item.ContentText,
			ContentMD:   item.ContentMD,
			Highlights:  highlights[item.ID],
			Media:       media[item.ID],
		}
		if item.DeletedAt != nil {
			deleted := stamp(*item.DeletedAt)
			entry.DeletedAt = &deleted
		}
		if entry.Tags == nil {
			entry.Tags = []string{}
		}
		if entry.Spaces == nil {
			entry.Spaces = []string{}
		}
		if entry.Highlights == nil {
			entry.Highlights = []highlightEntry{}
		}
		if entry.Media == nil {
			entry.Media = []mediaEntry{}
		}
		if item.TranscriptText != "" || item.TranscriptState != "" {
			entry.Transcript = &transcriptEntry{
				Text:  item.TranscriptText,
				Lang:  item.TranscriptLang,
				State: item.TranscriptState,
			}
		}
		entries = append(entries, entry)
	}
	return entries
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// rawOrEmpty keeps a stored JSON column as JSON rather than a string of JSON,
// so the export reads like the data it came from.
func rawOrEmpty(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}
