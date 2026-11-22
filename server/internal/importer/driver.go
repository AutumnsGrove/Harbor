// The import driver: a parsed row becomes a library item the same way a save
// does (design.md §5, plans/05). It canonicalises the URL, merges rather than
// duplicating, files the folder as a manual Space, writes the highlights, and
// leaves enrichment as a backlog — it never calls a model.
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/AutumnsGrove/Harbor/server/internal/canon"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// maxRowErrors bounds the report. A file can fail on every row; the dry run is
// where a human reads the reasons, so the first twenty are enough to see the
// pattern and the count says how bad it is.
const maxRowErrors = 20

// Source is a parsed bookmark file: the CSV or HTML reader, behind the two
// questions the report needs to ask it.
type Source interface {
	Next() (Row, error)
	UnknownColumns() []string
	Warnings() []string
}

// NewCSVSource reads a Raindrop CSV export.
func NewCSVSource(r io.Reader) (Source, error) { return NewCSVReader(r) }

// Options is what the CLI passes.
type Options struct {
	// DryRun reports what would happen and writes nothing. This is what gets
	// run against the real export before anything is committed.
	DryRun bool
	// NoEnrich writes items that are usable immediately (state ready) instead
	// of queueing them for the model, which is the documented choice for a bulk
	// import: 1,500 items through Nova is a bill and a long drain.
	NoEnrich bool
	// Limit stops after this many rows; zero means the whole file.
	Limit int
}

// RowError is one row that could not become an item, with the record number a
// human can find in the file.
type RowError struct {
	Record int
	URL    string
	Reason string
}

// Report is the run's accounting, and the JSON shape Settings → Import shows.
type Report struct {
	Total    int `json:"total"`
	Imported int `json:"imported"`
	Merged   int `json:"merged"`
	Skipped  int `json:"skipped"`
	Failed   int `json:"failed"`

	SpacesToCreate int `json:"spaces_to_create,omitempty"`
	SpacesCreated  int `json:"spaces_created,omitempty"`
	Favourites     int `json:"favourites"`
	Highlights     int `json:"highlights"`

	UnknownColumns []string   `json:"unknown_columns,omitempty"`
	FileWarnings   []string   `json:"file_warnings,omitempty"`
	Errors         []RowError `json:"errors,omitempty"`
}

// Run imports every row the source yields.
//
// It stops only for an error it cannot attribute to a row — a reader that cannot
// continue at all. A row that cannot become an item is counted, reported, and
// the run carries on: a file with three bad rows in it is still 1,473 saves
// worth having (plans/05 test 3).
func Run(ctx context.Context, s *store.Store, src Source, opts Options) (Report, error) {
	r := &runner{store: s, ctx: ctx, opts: opts}
	r.report.UnknownColumns = src.UnknownColumns()
	r.favourites, r.err = s.SpaceBySlug(ctx, "favourites")
	if r.err != nil {
		return r.report, fmt.Errorf("the seeded Favourites Space is missing: %w", r.err)
	}

	for opts.Limit <= 0 || r.report.Total < opts.Limit {
		row, err := src.Next()
		switch {
		case errors.Is(err, io.EOF):
			r.flush()
			r.report.FileWarnings = append(r.report.FileWarnings, src.Warnings()...)
			return r.report, nil
		case err != nil:
			return r.report, fmt.Errorf("reading the import file: %w", err)
		}
		r.report.Total++
		r.row(row)
	}
	r.flush()
	r.report.FileWarnings = append(r.report.FileWarnings, src.Warnings()...)
	return r.report, nil
}

type runner struct {
	store      *store.Store
	ctx        context.Context
	opts       Options
	report     Report
	favourites model.Space
	err        error

	// folders and spaces exist for the dry run: which names would become a
	// Space, and which have been counted already.
	folders map[string]bool

	// pending is each canonical URL's item and the highlights the whole file
	// carries for it. They are written once, after the last row: writing them
	// per row would delete the set a later row is about to add — which made a
	// second pass rewrite rows the first pass had already written.
	pending map[string]*pendingImport
}

type pendingImport struct {
	itemID string
	record int
	url    string
	quotes []string
}

// row turns one record into an item, or explains why it could not.
func (r *runner) row(row Row) {
	if strings.TrimSpace(row.URL) == "" {
		// A bookmark export's row without a URL is not a note: Raindrop always
		// writes one, so this is a broken row and it is counted as skipped.
		r.report.Skipped++
		return
	}
	canonical, err := canon.CanonicalURL(row.URL)
	if err != nil {
		r.fail(row, err)
		return
	}

	r.accumulate(canonical, row)
	existing, found, err := r.existing(canonical)
	if err != nil {
		r.fail(row, err)
		return
	}

	if r.opts.DryRun {
		if found {
			r.report.Merged++
		} else {
			r.report.Imported++
		}
		r.report.Highlights += len(row.Highlights)
		if row.Favorite {
			r.report.Favourites++
		}
		r.countFolder(row)
		return
	}

	if found {
		r.report.Merged++
	} else {
		r.report.Imported++
	}
	if _, err := r.write(row, canonical, existing, found); err != nil {
		r.fail(row, err)
		return
	}
	r.report.Highlights += len(row.Highlights)
	if row.Favorite {
		r.report.Favourites++
	}
}

// write is the save path: one item, its tags, its folder, the favourites Space,
// and the highlights, all through the store's own functions so an import cannot
// invent a second way to write a bookmark.
func (r *runner) write(row Row, canonical string, existing model.Item, found bool) (model.Item, error) {
	in := store.ItemInput{
		ID:          uuid.NewString(),
		URL:         &canonical,
		URLOriginal: row.URL,
		Title:       row.Title,
		Note:        row.Note,
		Source:      model.SourceImport,
		ImportRef:   row.ID,
		Priority:    -1,
		Tags:        row.Tags,
		OGJSON:      ogJSON(row),
		CreatedAt:   row.Created,
	}

	// An import preserves the original date, but it must not drag an item the
	// owner saved yesterday back to 2019: on a merge, saved_at stays put.
	if !found {
		in.SavedAt = row.Created
	} else {
		in.SavedAt = existing.SavedAt
	}

	item, _, err := r.store.MergeItem(r.ctx, in)
	if err != nil {
		return model.Item{}, err
	}

	if p, ok := r.pending[canonical]; ok {
		p.itemID = item.ID
	}

	if row.Folder != "" {
		space, err := r.store.EnsureManualSpace(r.ctx, row.Folder)
		if err != nil {
			return model.Item{}, err
		}
		if err := r.store.AddItemToSpace(r.ctx, space.ID, item.ID); err != nil {
			return model.Item{}, err
		}
	}
	if row.Favorite {
		if err := r.store.AddItemToSpace(r.ctx, r.favourites.ID, item.ID); err != nil {
			return model.Item{}, err
		}
	}

	// --no-enrich writes an item that is usable now: the link, note, tags and
	// highlights are what the owner came for, and the model can wait.
	if r.opts.NoEnrich {
		if err := r.store.MarkReady(r.ctx, item.ID); err != nil {
			return model.Item{}, err
		}
	}
	return item, nil
}

// accumulate records this row's quotes against its URL. The set is per run, so a
// file that no longer has a highlight removes it at flush time.
func (r *runner) accumulate(canonical string, row Row) {
	if r.pending == nil {
		r.pending = map[string]*pendingImport{}
	}
	p, ok := r.pending[canonical]
	if !ok {
		p = &pendingImport{record: row.Record, url: row.URL}
		r.pending[canonical] = p
	}
	for _, highlight := range row.Highlights {
		quote := strings.TrimSpace(highlight.Quote)
		if quote == "" || containsString(p.quotes, quote) {
			continue
		}
		p.quotes = append(p.quotes, quote)
	}
}

// flush writes each item's highlights once the whole file has been read. An
// error is attributed to the first row that contributed to that URL.
func (r *runner) flush() {
	for _, p := range r.pending {
		if p.itemID == "" {
			continue
		}
		if err := r.store.ReplaceImportedHighlights(r.ctx, p.itemID, p.quotes); err != nil {
			r.fail(Row{Record: p.record, URL: p.url}, err)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// existing looks the canonical URL up. A miss is not an error here: it is the
// insert case, and the store's unique index is still the authority.
func (r *runner) existing(canonical string) (model.Item, bool, error) {
	item, err := r.store.GetItemByURL(r.ctx, canonical)
	switch {
	case err == nil:
		return item, true, nil
	case errors.Is(err, store.ErrNotFound):
		return model.Item{}, false, nil
	default:
		return model.Item{}, false, err
	}
}

// countFolder is the dry run's "spaces to create": a folder that already exists
// is not new, and each name is counted once.
func (r *runner) countFolder(row Row) {
	name := strings.TrimSpace(row.Folder)
	if name == "" {
		return
	}
	key := strings.ToLower(name)
	if r.folders == nil {
		r.folders = map[string]bool{}
	}
	if r.folders[key] {
		return
	}
	r.folders[key] = true
	if _, found, err := r.store.FindManualSpace(r.ctx, name); err == nil && !found {
		r.report.SpacesToCreate++
	}
}

func (r *runner) fail(row Row, err error) {
	r.report.Failed++
	if len(r.report.Errors) < maxRowErrors {
		r.report.Errors = append(r.report.Errors, RowError{Record: row.Record, URL: row.URL, Reason: err.Error()})
	}
}

// ogJSON keeps the page's description and cover without touching the owner's
// note (design.md §5: `excerpt` and `cover` go to og_json).
func ogJSON(row Row) string {
	og := map[string]string{}
	if excerpt := strings.TrimSpace(row.Excerpt); excerpt != "" {
		og["excerpt"] = excerpt
	}
	if cover := strings.TrimSpace(row.Cover); cover != "" {
		og["cover"] = cover
	}
	if len(og) == 0 {
		return ""
	}
	encoded, err := json.Marshal(og)
	if err != nil {
		return ""
	}
	return string(encoded)
}
