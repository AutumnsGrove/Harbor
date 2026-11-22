// Package importer reads the bookmark exports a person actually has into rows
// the import driver can merge (design.md §5, plans/05): Raindrop's CSV and
// Netscape bookmark HTML. It parses and reports; it never writes to the
// database and it never trusts the file.
//
// Nothing here canonicalises a URL or knows what a Space is — the driver does
// that, so this package stays small enough to fuzz.
package importer

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// ErrNotRaindropCSV means the file cannot be a Raindrop CSV export at all: it is
// empty, or its header is missing, or it is missing the one column that a
// bookmark export cannot do without. It is never returned for a bad *row*.
// Spike S9 is the source of the column list.
var ErrNotRaindropCSV = errors.New("importer: not a Raindrop CSV export")

// raindropColumns are the eleven columns the real export carries, in its order
// (spike S9). Reading is header-driven, so order, case and padding do not
// matter; this list exists to tell a known column from one Raindrop added later.
var raindropColumns = []string{
	"id", "title", "note", "excerpt", "url", "folder", "tags", "created", "cover", "highlights", "favorite",
}

// requiredColumns is what a file must have to be a Raindrop export. Everything
// else has a sensible absence: no title, no date, no tags is a row, not an error.
var requiredColumns = []string{"url"}

// Highlight is one highlighted passage from a row's `highlights` cell. Index is
// its 0-based position in the cell, so a rejoined set reproduces the original.
type Highlight struct {
	Index int
	Quote string
}

// Row is one CSV record after mapping to Raindrop's column names.
//
// It is deliberately permissive: a record with no URL and no title is returned
// (empty, with a warning) rather than dropped, because the import report counts
// it and the driver is the only thing that knows what "skipped" means. Warnings
// are the row's, not the file's — the file's are on the reader.
type Row struct {
	// Record is the 1-based data record number, counting the header as
	// nothing; it is what a report cites when a row goes wrong.
	Record int

	ID         string
	Title      string
	Note       string
	Excerpt    string
	URL        string
	Folder     string
	Tags       []string
	Created    time.Time // zero when absent or unreadable
	Cover      string
	Highlights []Highlight
	Favorite   bool

	Warnings []string

	// rawHighlights is the cell as read, before splitting. The driver stores
	// nothing from it; the round-trip test uses it to prove the split is lossless.
	rawHighlights string
}

const (
	// hlPrefix starts every highlight block in the real export (spike S9).
	hlPrefix = "Highlight:"
)

// CSVReader streams a Raindrop CSV export. One row at a time, because a real
// library is not small and the driver has a checkpoint to honour.
type CSVReader struct {
	r     *csv.Reader
	width int // the header's actual field count, blank and duplicate names included

	record  int
	columns []string // the recognised names, in file order
	index   map[string]int
	unknown []string
	warns   []string

	lastErrAt int64
}

// NewCSVReader reads and validates the header. The only errors are the typed
// "this is not a Raindrop export" ones; a strange row is the row's problem.
func NewCSVReader(src io.Reader) (*CSVReader, error) {
	r := csv.NewReader(src)
	// Columns can change under us (spike S9's unknown-column report); a
	// mismatched field count is a warning on the row, not a read error.
	r.FieldsPerRecord = -1
	// Keep leading spaces: they are content in a note. Quotes are strict so a
	// malformed record is recognised as one instead of silently mis-parsed.
	r.TrimLeadingSpace = false

	cr := &CSVReader{r: r, index: map[string]int{}}
	head, err := r.Read()
	switch {
	case errors.Is(err, io.EOF):
		return nil, fmt.Errorf("%w: the file is empty", ErrNotRaindropCSV)
	case err != nil:
		return nil, fmt.Errorf("%w: %v", ErrNotRaindropCSV, err)
	}
	// The header's width, not the number of recognised names: a blank or
	// duplicate column is dropped from the mapping but still occupies a field.
	cr.width = len(head)

	for i, name := range head {
		name = normaliseHeader(name)
		if name == "" {
			continue
		}
		if _, seen := cr.index[name]; seen {
			cr.warns = append(cr.warns, fmt.Sprintf("column %q appears twice; the first one wins", name))
			continue
		}
		cr.index[name] = i
		cr.columns = append(cr.columns, name)
	}

	var missing []string
	for _, name := range requiredColumns {
		if _, ok := cr.index[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: the header has no %s column (expected %s)",
			ErrNotRaindropCSV, strings.Join(missing, " or "), strings.Join(raindropColumns, ","))
	}

	for _, name := range cr.columns {
		if !contains(raindropColumns, name) {
			cr.unknown = append(cr.unknown, name)
		}
	}
	return cr, nil
}

// UnknownColumns are header names the importer does not map, in file order. The
// dry-run summary lists them so a change in Raindrop's export is visible
// (plans/05) rather than silently dropped.
func (cr *CSVReader) UnknownColumns() []string { return cr.unknown }

// Warnings are file-level findings — a duplicated column — as opposed to a
// row's own.
func (cr *CSVReader) Warnings() []string { return cr.warns }

// Columns are the recognised header names, in file order.
func (cr *CSVReader) Columns() []string { return cr.columns }

// Next returns the next record, or io.EOF at the end of the file.
//
// A record whose CSV is malformed comes back as a warning with no fields rather
// than as an error: one bad line must never end a 1,500-row import.
// encoding/csv resumes at the next record. The only error Next returns
// mid-file is the one that cannot happen twice — a reader stuck at the same
// offset — which would otherwise spin forever.
func (cr *CSVReader) Next() (Row, error) {
	rec, err := cr.r.Read()
	switch {
	case errors.Is(err, io.EOF):
		return Row{}, io.EOF
	case err != nil:
		cr.record++
		at := cr.r.InputOffset()
		if at == cr.lastErrAt {
			return Row{}, fmt.Errorf("importer: the reader cannot advance past byte %d: %w", at, err)
		}
		cr.lastErrAt = at
		// Keep going: the rows around a broken one are still imported.
		return Row{Record: cr.record, Warnings: []string{fmt.Sprintf("unreadable CSV record: %v", err)}}, nil
	}
	cr.record++
	return cr.mapRecord(rec), nil
}

// mapRecord turns one record into a Row. Everything that is wrong with it is a
// warning here; what to do about it belongs to the driver.
func (cr *CSVReader) mapRecord(rec []string) Row {
	row := Row{Record: cr.record}

	// The header's width, not the number of recognised names: a duplicate or
	// blank column is dropped from the mapping, and a record with the same
	// fields as the header is not a ragged record.
	if len(rec) != cr.width {
		row.warn("record %d has %d fields, the header has %d", cr.record, len(rec), cr.width)
	}

	row.ID = cr.field(rec, "id")
	row.Title = cr.field(rec, "title")
	row.Note = cr.field(rec, "note")
	row.Excerpt = cr.field(rec, "excerpt")
	row.URL = strings.TrimSpace(cr.field(rec, "url"))
	row.Folder = cr.field(rec, "folder")
	row.Cover = cr.field(rec, "cover")
	row.Tags = parseTags(cr.field(rec, "tags"))

	if raw := cr.field(rec, "created"); strings.TrimSpace(raw) != "" {
		created, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
		if err != nil {
			row.warn("created %q is not an RFC 3339 timestamp; the item keeps no original date", raw)
		} else {
			row.Created = created.UTC()
		}
	}

	switch fav, ok := parseFavorite(cr.field(rec, "favorite")); {
	case !ok:
		row.warn("favorite %q is not a boolean; treated as false", cr.field(rec, "favorite"))
	default:
		row.Favorite = fav
	}

	if cell := cr.field(rec, "highlights"); strings.TrimSpace(cell) != "" {
		row.rawHighlights = normaliseNewlines(cell)
		row.Highlights = parseHighlights(row.rawHighlights)
		if !strings.HasPrefix(strings.TrimLeft(row.rawHighlights, "\n"), hlPrefix) {
			row.warn("highlights do not start with %q; kept as one quoted passage", hlPrefix)
		}
	}

	if row.URL == "" && strings.TrimSpace(row.Title) == "" {
		row.warn("record %d has neither a URL nor a title", cr.record)
	}
	return row
}

// field is the record's value for a header name. A name the header does not
// carry (or a record too short to reach it) is the empty string; the field
// count warning already said so.
func (cr *CSVReader) field(rec []string, name string) string {
	i, ok := cr.index[name]
	if !ok || i >= len(rec) {
		return ""
	}
	return rec[i]
}

func (r *Row) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// normaliseHeader strips the byte-order mark some exporters prepend, the
// padding a spreadsheet adds, and the case Raindrop might change one day.
func normaliseHeader(name string) string {
	name = strings.TrimPrefix(name, "\ufeff")
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))
}

// parseTags splits Raindrop's one-comma-separated cell. Case and spacing are
// left for store.NormaliseTag, which is the single tag write path: trimming is
// all this layer does.
func parseTags(cell string) []string {
	if strings.TrimSpace(cell) == "" {
		return nil
	}
	var out []string
	for _, tag := range strings.Split(cell, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

// parseHighlights splits a highlight cell into one quote per block.
//
// The real cell is prose (spike S9): blocks are marked by a line that starts
// with "Highlight:", and the quote inside a block may contain blank lines of its
// own. So a line scan, not a split on blank lines: a paragraph of a quote is
// still part of that quote. Rejoining with "\n\nHighlight:" reproduces the
// cell.
func parseHighlights(cell string) []Highlight {
	cell = normaliseNewlines(cell)
	if strings.TrimSpace(cell) == "" {
		return nil
	}

	var (
		out     []Highlight
		current []string
	)
	flush := func() {
		quote := strings.TrimSpace(strings.Join(current, "\n"))
		current = current[:0]
		if quote != "" {
			out = append(out, Highlight{Index: len(out), Quote: quote})
		}
	}
	for _, line := range strings.Split(cell, "\n") {
		if rest, ok := strings.CutPrefix(line, hlPrefix); ok {
			flush()
			current = append(current, rest)
			continue
		}
		current = append(current, line)
	}
	flush()
	return out
}

func normaliseNewlines(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// parseFavorite reads the flag Raindrop writes as `true`/`false`, tolerating the
// spellings a spreadsheet round-trip produces. ok is false when the value is
// neither, which is a warning rather than an error.
func parseFavorite(raw string) (value, ok bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "false", "0", "no":
		return false, true
	case "true", "1", "yes":
		return true, true
	default:
		return false, false
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
