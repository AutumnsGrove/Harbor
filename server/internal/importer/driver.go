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
	// CarriesHighlights says whether this format can express highlights at
	// all. A source that cannot must never clear highlights another import
	// wrote: Raindrop's CSV has them, Netscape HTML does not.
	CarriesHighlights() bool
}

// NewCSVSource reads a Raindrop CSV export.
func NewCSVSource(r io.Reader) (Source, error) { return NewCSVReader(r) }

// NewHTMLSource reads a Netscape bookmark file: what a browser exports, and what
// phase 12a's export writes. It carries no highlights, and the driver honours
// that rather than treating the absence as a deletion.
func NewHTMLSource(r io.Reader) (Source, error) { return NewHTMLReader(r) }

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

	// SkipRows is a resume: this many rows were already read by an earlier run
	// of the same file, so they are skipped and the merge rule handles the rest.
	SkipRows int

	// CheckpointEvery asks for OnCheckpoint every N rows. The highlights read so
	// far are flushed first, so a checkpoint never gets ahead of the data.
	CheckpointEvery int
	OnCheckpoint    func(records int, report Report) error
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
	r := &runner{store: s, ctx: ctx, opts: opts, carriesHighlights: src.CarriesHighlights()}
	r.report.UnknownColumns = src.UnknownColumns()
	r.favourites, r.err = s.SpaceBySlug(ctx, "favourites")
	if r.err != nil {
		return r.report, fmt.Errorf("the seeded Favourites Space is missing: %w", r.err)
	}

	// A resume does not re-apply the rows an earlier run wrote, but it does read
	// them: the file is the truth for a URL's highlights, and a URL can appear on
	// both sides of the checkpoint. Skipping the bytes without remembering their
	// quotes would let flush() delete what the first pass wrote.
	for skipped := 0; skipped < opts.SkipRows; skipped++ {
		row, err := src.Next()
		if err != nil {
			break
		}
		r.rememberSkipped(row)
	}

	for opts.Limit <= 0 || r.report.Total < opts.Limit {
		if err := r.ctx.Err(); err != nil {
			// Stop where we are and keep what was written: the checkpoint says
			// how far, and the next run starts from there.
			r.flush()
			return r.report, err
		}
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
		if err := r.row(row); err != nil {
			// The context died while writing this row: stop here rather than
			// count it as a failure, because everything after it is unprocessed
			// and the resume re-reads from the checkpoint.
			r.flush()
			return r.report, err
		}
		if err := r.checkpoint(); err != nil {
			return r.report, err
		}
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

	// carriesHighlights is the source's answer, so a format without highlights
	// leaves the ones already in the library alone when flushed.
	carriesHighlights bool

	// seen is the dry run's memory of which canonical URLs it has counted: the
	// second row for one URL is a merge, not a second import.
	seen map[string]bool

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
	// fromSkipped marks an entry whose only contributions were rows a resumed
	// run skipped: its item has to be looked up at flush time, because nothing
	// in this run wrote it.
	fromSkipped bool
}

// row turns one record into an item, or explains why it could not.
//
// It returns an error only when the run itself has to stop — a cancelled
// context mid-write — because a row that has been applied must never be
// reported as failed while the resume skips past it.
func (r *runner) row(row Row) error {
	r.noteWarnings(row)
	if strings.TrimSpace(row.URL) == "" {
		// A bookmark export's row without a URL is not a note: Raindrop always
		// writes one, so this is a broken row and it is counted as skipped.
		r.report.Skipped++
		return nil
	}

	canonical, err := canon.CanonicalURL(row.URL)
	if err != nil {
		r.fail(row, err)
		return nil
	}

	r.accumulate(canonical, row)
	existing, found, err := r.existing(canonical)
	if err != nil {
		return r.rowError(row, err)
	}

	if r.opts.DryRun {
		r.dryRunRow(row, canonical, found)
		return nil
	}

	// Count only what happened: a row whose write fails is a failure, not an
	// import as well.
	_, merged, err := r.write(row, canonical, existing, found)
	if err != nil {
		return r.rowError(row, err)
	}
	if merged {
		r.report.Merged++
	} else {
		r.report.Imported++
	}
	r.report.Highlights += len(row.Highlights)
	if row.Favorite {
		r.report.Favourites++
	}
	return nil
}

// rowError separates the two kinds of failure. A cancelled context is the run
// stopping, and it must propagate: the checkpoint is behind this row, so a
// resume will read it again. Anything else is this row's own problem, counted
// and reported while the run carries on.
func (r *runner) rowError(row Row, err error) error {
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	r.fail(row, err)
	return nil
}

// rememberSkipped keeps a resumed run's view of the file complete without
// applying it: the quotes are accumulated, the row is not written. A row that
// could not have been imported the first time is ignored here too.
func (r *runner) rememberSkipped(row Row) {
	canonical, err := canon.CanonicalURL(row.URL)
	if err != nil {
		return
	}
	r.accumulate(canonical, row)
	if p, ok := r.pending[canonical]; ok {
		p.fromSkipped = true
	}
}

// dryRunRow reports what the real run would do, without writing anything. A URL
// the file already carried is a merge, exactly as it is in the real run.
func (r *runner) dryRunRow(row Row, canonical string, found bool) {
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	if found || r.seen[canonical] {
		r.report.Merged++
	} else {
		r.report.Imported++
	}
	r.seen[canonical] = true
	r.report.Highlights += len(row.Highlights)
	if row.Favorite {
		r.report.Favourites++
	}
	r.countFolder(row)
}

// noteWarnings puts the reader's row-level findings in the report: a date that
// will not parse is exactly what the dry run exists to show before anything is
// written. The list is bounded like the row errors.
func (r *runner) noteWarnings(row Row) {
	for _, warning := range row.Warnings {
		if len(r.report.FileWarnings) >= maxRowErrors {
			return
		}
		r.report.FileWarnings = append(r.report.FileWarnings, fmt.Sprintf("record %d: %s", row.Record, warning))
	}
}

// write is the save path: one item, its tags, its folder, the favourites Space,
// and the highlights, all through the store's own functions so an import cannot
// invent a second way to write a bookmark.
func (r *runner) write(row Row, canonical string, existing model.Item, found bool) (model.Item, bool, error) {
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

	item, merged, err := r.store.MergeItem(r.ctx, in)
	if err != nil {
		return model.Item{}, false, err
	}

	if p, ok := r.pending[canonical]; ok {
		p.itemID = item.ID
	}

	if row.Folder != "" {
		// Ask before creating, so the report can say how many Spaces this run
		// made rather than how many folders it saw.
		_, exists, err := r.store.FindManualSpace(r.ctx, row.Folder)
		if err != nil {
			return model.Item{}, false, err
		}
		space, err := r.store.EnsureManualSpace(r.ctx, row.Folder)
		if err != nil {
			return model.Item{}, false, err
		}
		if !exists {
			r.report.SpacesCreated++
		}
		if err := r.store.AddItemToSpace(r.ctx, space.ID, item.ID); err != nil {
			return model.Item{}, false, err
		}
	}
	if row.Favorite {
		if err := r.store.AddItemToSpace(r.ctx, r.favourites.ID, item.ID); err != nil {
			return model.Item{}, false, err
		}
	}

	// --no-enrich writes an item that is usable now: the link, note, tags and
	// highlights are what the owner came for, and the model can wait.
	if r.opts.NoEnrich {
		if err := r.store.MarkReady(r.ctx, item.ID); err != nil {
			return model.Item{}, false, err
		}
	}
	return item, merged, nil
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

// checkpoint flushes what has been read and reports how far the run got. The
// flush comes first: a checkpoint that ran ahead of the highlights would let a
// resume skip the rows whose highlights were never written.
func (r *runner) checkpoint() error {
	if r.opts.CheckpointEvery <= 0 || r.opts.OnCheckpoint == nil {
		return nil
	}
	if r.report.Total%r.opts.CheckpointEvery != 0 {
		return nil
	}
	r.flush()
	return r.opts.OnCheckpoint(r.opts.SkipRows+r.report.Total, r.report)
}

// flush writes each item's highlights once the whole file has been read. An
// error is attributed to the first row that contributed to that URL.
//
// A source that cannot express highlights (Netscape HTML) never reaches the
// store here: it is imported over a library that already has them, and clearing
// them would destroy the one thing the owner cannot recreate.
func (r *runner) flush() {
	// A dry run writes nothing, highlights included: its pending entries are the
	// preview, and filling in an item id here would let it overwrite a real item.
	//
	// A cancelled run has nothing left to write: the flush after a stop would
	// fail on the dead context and report it as a row failure, which is exactly
	// the false failure the checkpoint exists to avoid.
	if r.opts.DryRun || !r.carriesHighlights || r.ctx.Err() != nil {
		return
	}
	for canonical, p := range r.pending {
		if p.itemID == "" {
			// Only rows this run skipped can leave an entry without an item: look
			// it up rather than let the URL go unwritten. Anything else is a row
			// whose write failed, and that is already in the report.
			if !p.fromSkipped {
				continue
			}
			item, found, err := r.existing(canonical)
			if err != nil || !found {
				continue
			}
			p.itemID = item.ID
		}
		if err := r.store.ReplaceImportedHighlights(r.ctx, p.itemID, p.quotes); err != nil {
			// A flush cut short by the interrupt is not the row's fault, and the
			// resume rebuilds the set from the file's own rows: reporting it as a
			// failure would be a lie in the report.
			if r.ctx.Err() != nil {
				return
			}
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
