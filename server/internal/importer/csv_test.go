package importer

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// readAll drains a reader the way the driver does.
func readAll(t *testing.T, src string) []Row {
	t.Helper()
	cr, err := NewCSVReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("NewCSVReader: %v", err)
	}
	var rows []Row
	for {
		row, err := cr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		rows = append(rows, row)
	}
	return rows
}

// header is the real export's header line (spike S9).
const header = "id,title,note,excerpt,url,folder,tags,created,cover,highlights,favorite\n"

func one(t *testing.T, body string) Row {
	t.Helper()
	rows := readAll(t, header+body)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	return rows[0]
}

// The fixture carries the shapes the real export has (spike S9): multi-line
// notes, doubled quotes, a noisy URL, multi-block highlights, unicode, an empty
// title and a duplicate URL. It is the one test allowed to assert content.
func TestReadRealShape(t *testing.T) {
	f, err := os.Open("testdata/raindrop/real-shape.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cr, err := NewCSVReader(f)
	if err != nil {
		t.Fatalf("NewCSVReader: %v", err)
	}
	if unknown := cr.UnknownColumns(); len(unknown) != 0 {
		t.Errorf("unknown columns in the real export: %v", unknown)
	}

	var rows []Row
	for {
		row, err := cr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		rows = append(rows, row)
	}
	if len(rows) != 15 {
		t.Fatalf("got %d rows, want 15", len(rows))
	}

	first := rows[0]
	if first.ID != "1000000001" || first.Folder != "Articles" {
		t.Errorf("row 1 identity: id=%q folder=%q", first.ID, first.Folder)
	}
	if first.URL != "https://example.com/articles/hostile-csv" {
		t.Errorf("row 1 URL = %q", first.URL)
	}
	if want := []string{"parsing", "testing"}; !equalSlices(first.Tags, want) {
		t.Errorf("row 1 tags = %v, want %v", first.Tags, want)
	}
	wantCreated := time.Date(2024, 11, 2, 9, 14, 31, 512_000_000, time.UTC)
	if !first.Created.Equal(wantCreated) {
		t.Errorf("row 1 created = %v, want %v", first.Created, wantCreated)
	}
	if first.Favorite {
		t.Error("row 1 is not a favourite")
	}

	// Embedded blank lines in a note survive; the field is not folded.
	second := rows[1]
	if !strings.Contains(second.Note, "first line\n\nsecond paragraph") {
		t.Errorf("row 2 note lost its blank line: %q", second.Note)
	}
	if !strings.HasSuffix(second.Note, "a comma\n") {
		t.Errorf("row 2 note lost its trailing newline: %q", second.Note)
	}
	if len(second.Tags) != 5 {
		t.Errorf("row 2 tags = %v, want five (case variants are kept for the store)", second.Tags)
	}
	if !second.Favorite {
		t.Error("row 2 is a favourite")
	}

	// Doubled quotes in a title and quotes inside a note.
	third := rows[2]
	if want := `He said "parse it twice", then shipped it`; third.Title != want {
		t.Errorf("row 3 title = %q, want %q", third.Title, want)
	}
	if !strings.Contains(third.Note, `"inner"`) {
		t.Errorf("row 3 note lost its quotes: %q", third.Note)
	}

	// A noisy URL is handed on unmodified: canonicalisation is the driver's job.
	fourth := rows[3]
	if !strings.Contains(fourth.URL, "utm_source=newsletter") || !strings.HasSuffix(fourth.URL, "#reviews") {
		t.Errorf("row 4 URL = %q, want the raw value", fourth.URL)
	}
	if fourth.Folder != "Read Later" {
		t.Errorf("row 4 folder = %q", fourth.Folder)
	}

	// Highlight cells: one, two, four and one (junk) blocks.
	if got := len(rows[4].Highlights); got != 1 {
		t.Errorf("row 5 highlights = %d, want 1", got)
	}
	if got := len(rows[5].Highlights); got != 2 {
		t.Errorf("row 6 highlights = %d, want 2 (blank lines inside a block are not separators)", got)
	}
	if got := len(rows[6].Highlights); got != 4 {
		t.Errorf("row 7 highlights = %d, want 4", got)
	}
	if got := rows[6].Highlights[1].Quote; got != "tiny" {
		t.Errorf("row 7 highlight 2 = %q, want \"tiny\"", got)
	}
	if got := rows[6].Highlights[2].Quote; !strings.Contains(got, "line break\nright through") {
		t.Errorf("row 7 highlight 3 lost its internal newline: %q", got)
	}

	if got := rows[7].Title; !strings.Contains(got, "🎈") || !strings.Contains(got, "日本語") {
		t.Errorf("row 8 title lost its unicode: %q", got)
	}
	if rows[8].Title != "" {
		t.Errorf("row 9 title = %q, want empty", rows[8].Title)
	}
	if rows[9].URL != rows[0].URL {
		t.Errorf("row 10 should repeat row 1's URL: %q", rows[9].URL)
	}
	if want := []string{"alpha", "beta", "gamma"}; !equalSlices(rows[10].Tags, want) {
		t.Errorf("row 11 tags = %v, want %v", rows[10].Tags, want)
	}

	for _, row := range rows {
		if len(row.Warnings) != 0 {
			t.Errorf("row %d of the real shape warns: %v", row.Record, row.Warnings)
		}
	}

	// Rejoining a row's highlights reproduces the cell (design.md §5): the
	// import is lossless, which is the whole point of not guessing. This holds
	// for the export's shape — one blank line between blocks and no stray
	// whitespace around them — which is what the fixture is.
	for _, row := range rows {
		if len(row.Highlights) == 0 {
			continue
		}
		quotes := make([]string, len(row.Highlights))
		for i, h := range row.Highlights {
			quotes[i] = h.Quote
		}
		if got := "Highlight:" + strings.Join(quotes, "\n\nHighlight:"); got != row.rawHighlights {
			t.Errorf("row %s highlights do not rejoin: got %q, want %q", row.ID, got, row.rawHighlights)
		}
	}
}

// Header handling: a BOM, CRLF line endings, reordered and differently-cased
// names, and padding all have to land on the same columns.
func TestCSVHeaderTolerance(t *testing.T) {
	src := "\ufeffURL , Title,ID ,TAGS,Note,Created\r\n" +
		"https://example.com/a,A title,9,one\\two,a note,2024-01-02T03:04:05.006Z\r\n"
	cr, err := NewCSVReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("NewCSVReader: %v", err)
	}
	row, err := cr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if row.URL != "https://example.com/a" || row.Title != "A title" || row.ID != "9" {
		t.Errorf("row = %+v", row)
	}
	if want := []string{`one\two`}; !equalSlices(row.Tags, want) {
		t.Errorf("tags = %v, want %v", row.Tags, want)
	}
	if row.Created.Year() != 2024 {
		t.Errorf("created = %v", row.Created)
	}
	if len(row.Warnings) != 0 {
		t.Errorf("warnings: %v", row.Warnings)
	}
}

// A column Raindrop adds later is reported, never silently coerced, and its
// values do not disturb the ones we know (plans/05).
func TestCSVUnknownColumns(t *testing.T) {
	src := header + "1,t,,,https://example.com/a,F,x,2024-01-02T03:04:05Z,,,false,3\n"
	src = strings.Replace(src, "\n", ",type\n", 1)

	cr, err := NewCSVReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("NewCSVReader: %v", err)
	}
	if got, want := cr.UnknownColumns(), []string{"type"}; !equalSlices(got, want) {
		t.Errorf("unknown columns = %v, want %v", got, want)
	}
	row, err := cr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if row.URL != "https://example.com/a" {
		t.Errorf("URL = %q", row.URL)
	}
	if len(row.Warnings) != 0 {
		t.Errorf("an unknown column is a file-level fact, not a row warning: %v", row.Warnings)
	}
}

// A file with no header is a typed error naming what it wanted, never a
// positional guess (spike S9).
func TestCSVHeaderlessIsTypedError(t *testing.T) {
	_, err := NewCSVReader(strings.NewReader("1,A title,,,https://example.com/a,Articles,x,2024-01-02T03:04:05Z,,,false\n"))
	if !errors.Is(err, ErrNotRaindropCSV) {
		t.Fatalf("err = %v, want ErrNotRaindropCSV", err)
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("error should name the missing column: %v", err)
	}
}

func TestCSVEmptyFileIsTypedError(t *testing.T) {
	for _, src := range []string{"", "\n", "\ufeff"} {
		if _, err := NewCSVReader(strings.NewReader(src)); !errors.Is(err, ErrNotRaindropCSV) {
			t.Errorf("%q: err = %v, want ErrNotRaindropCSV", src, err)
		}
	}
}

// A header-only file is a valid, empty import.
func TestCSVHeaderOnly(t *testing.T) {
	cr, err := NewCSVReader(strings.NewReader(header))
	if err != nil {
		t.Fatalf("NewCSVReader: %v", err)
	}
	if _, err := cr.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want io.EOF", err)
	}
}

// Quoted commas, newlines and doubled quotes are the reason this is not a
// strings.Split.
func TestCSVQuoting(t *testing.T) {
	row := one(t, "1,\"A title, with a comma\",\"note, with \"\"doubled\"\" quotes\",,\"https://example.com/a?x=1,2\",F,\"tag one, tag two\",2024-01-02T03:04:05Z,,,false\n")
	if row.Title != "A title, with a comma" {
		t.Errorf("title = %q", row.Title)
	}
	if row.Note != `note, with "doubled" quotes` {
		t.Errorf("note = %q", row.Note)
	}
	if row.URL != "https://example.com/a?x=1,2" {
		t.Errorf("url = %q", row.URL)
	}
	if want := []string{"tag one", "tag two"}; !equalSlices(row.Tags, want) {
		t.Errorf("tags = %v, want %v", row.Tags, want)
	}
}

// Short and long records are warnings, not lost rows: the driver still decides
// what to do with them.
func TestCSVRaggedRecords(t *testing.T) {
	rows := readAll(t, header+
		"1,short,,,https://example.com/a\n"+
		"2,long,,,https://example.com/b,F,x,2024-01-02T03:04:05Z,,,false,extra,more\n")

	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0].URL != "https://example.com/a" || rows[0].Title != "short" {
		t.Errorf("short record = %+v", rows[0])
	}
	if !hasWarning(rows[0].Warnings, "field") {
		t.Errorf("short record should warn about the field count: %v", rows[0].Warnings)
	}
	if rows[1].URL != "https://example.com/b" || rows[1].Favorite {
		t.Errorf("long record = %+v", rows[1])
	}
	if !hasWarning(rows[1].Warnings, "field") {
		t.Errorf("long record should warn about the extra fields: %v", rows[1].Warnings)
	}
}

// A second `url` column must not make the mapping ambiguous.
func TestCSVDuplicateColumn(t *testing.T) {
	src := "id,title,url,folder,url,created\n1,A,https://example.com/first,F,https://example.com/second,2024-01-02T03:04:05Z\n"
	cr, err := NewCSVReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("NewCSVReader: %v", err)
	}
	if !hasWarning(cr.Warnings(), "url") {
		t.Errorf("duplicate column should warn: %v", cr.Warnings())
	}
	row, err := cr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if row.URL != "https://example.com/first" {
		t.Errorf("url = %q, want the first column", row.URL)
	}
	// A well-formed record of a header with a repeated column is not ragged:
	// the report must not claim a data-integrity problem on every row.
	if hasWarning(row.Warnings, "field") {
		t.Errorf("the duplicate column made a well-formed row look ragged: %v", row.Warnings)
	}
}

// A spreadsheet round-trip leaves a trailing empty column. Like a duplicate
// name, it is dropped from the mapping and must not warn on every row.
func TestCSVTrailingEmptyColumn(t *testing.T) {
	src := "id,title,url,folder,created,\n1,A,https://example.com/a,F,2024-01-02T03:04:05Z,\n"
	cr, err := NewCSVReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("NewCSVReader: %v", err)
	}
	row, err := cr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if row.URL != "https://example.com/a" || row.Title != "A" {
		t.Errorf("row = %+v", row)
	}
	if len(row.Warnings) != 0 {
		t.Errorf("a trailing empty column is not a ragged record: %v", row.Warnings)
	}
	if got, want := cr.Columns(), []string{"id", "title", "url", "folder", "created"}; !equalSlices(got, want) {
		t.Errorf("columns = %v, want %v", got, want)
	}
}

// A value the format says cannot be there is a warning and a zero, never a
// dropped row: the item is more valuable than its metadata.
func TestCSVBadValues(t *testing.T) {
	row := one(t, "1,A,,,https://example.com/a,F,,not-a-date,,,maybe\n")
	if !row.Created.IsZero() {
		t.Errorf("created = %v, want zero", row.Created)
	}
	if row.Favorite {
		t.Error("favorite = true for a value the parser could not read")
	}
	if !hasWarning(row.Warnings, "created") {
		t.Errorf("warnings should name the date: %v", row.Warnings)
	}
	if !hasWarning(row.Warnings, "favorite") {
		t.Errorf("warnings should name the flag: %v", row.Warnings)
	}
}

// `true`/`false` are what Raindrop writes (spike S9); the tolerant spellings
// cost nothing and a spreadsheet round-trip produces them.
func TestCSVFavoriteSpellings(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"true", true}, {"false", false}, {"TRUE", true}, {"1", true}, {"0", false},
		{" yes ", true}, {"", false},
	} {
		row := readAll(t, header+"1,A,,,https://example.com/a,F,,2024-01-02T03:04:05Z,,,"+tc.in+"\n")[0]
		if row.Favorite != tc.want {
			t.Errorf("%q: favorite = %v, want %v", tc.in, row.Favorite, tc.want)
		}
	}
}

// Rows with neither a URL nor a title come back empty and warned rather than
// swallowed, so the driver can count them as skipped (plans/05).
func TestCSVEmptyRowIsReturned(t *testing.T) {
	row := one(t, ",,,, ,,,2024-01-02T03:04:05Z,,,false\n")
	if row.URL != "" || row.Title != "" {
		t.Errorf("row = %+v, want an empty URL and title", row)
	}
	if !hasWarning(row.Warnings, "URL") {
		t.Errorf("warnings should say the row has neither URL nor title: %v", row.Warnings)
	}
}

// A quote inside an unquoted field is malformed CSV. The run must survive it
// and keep the rows around it: this is the fuzzer's property, pinned with a
// concrete case.
func TestCSVUnterminatedQuoteDoesNotAbort(t *testing.T) {
	src := header +
		"1,good,,,https://example.com/a,F,,2024-01-02T03:04:05Z,,,false\n" +
		"2,bad\"quote,,,https://example.com/b,F,,2024-01-02T03:04:05Z,,,false\n" +
		"3,good again,,,https://example.com/c,F,,2024-01-02T03:04:05Z,,,false\n"
	rows := readAll(t, src)

	var kept []string
	for _, row := range rows {
		kept = append(kept, row.URL)
	}
	if len(kept) != 3 || kept[0] != "https://example.com/a" || kept[2] != "https://example.com/c" {
		t.Fatalf("the rows around a malformed one were lost: %v", kept)
	}
	if !hasWarning(rows[1].Warnings, "CSV") {
		t.Errorf("the malformed record should carry a warning: %v", rows[1].Warnings)
	}
}

// Blank lines between records are not records (encoding/csv drops them, but a
// trailing newline at EOF must not produce an empty row either).
func TestCSVBlankLines(t *testing.T) {
	rows := readAll(t, header+
		"\n"+
		"1,A,,,https://example.com/a,F,,2024-01-02T03:04:05Z,,,false\n"+
		"\n\n"+
		"2,B,,,https://example.com/b,F,,2024-01-02T03:04:05Z,,,false\n\n")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Record != 1 || rows[1].Record != 2 {
		t.Errorf("record numbers = %d, %d, want 1, 2", rows[0].Record, rows[1].Record)
	}
}

func TestParseTags(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"one", []string{"one"}},
		{"one, two", []string{"one", "two"}},
		{"alpha,  beta , gamma,", []string{"alpha", "beta", "gamma"}},
		{",,,", nil},
		{"AI Costs, ai", []string{"AI Costs", "ai"}},
	} {
		if got := parseTags(tc.in); !equalSlices(got, tc.want) {
			t.Errorf("parseTags(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseHighlights(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace", "  \n \n ", nil},
		{"one", "Highlight:just one", []string{"just one"}},
		{"two", "Highlight:first\n\nHighlight:second", []string{"first", "second"}},
		{"blank lines inside a block are content",
			"Highlight:one\n\nstill one.\n\nHighlight:two", []string{"one\n\nstill one.", "two"}},
		{"three newlines still separate",
			"Highlight:one\n\n\nHighlight:two", []string{"one", "two"}},
		{"CRLF", "Highlight:one\r\n\r\nHighlight:two\r\n", []string{"one", "two"}},
		{"no prefix is one block", "a quote with no marker", []string{"a quote with no marker"}},
		{"empty block dropped", "Highlight:\n\nHighlight:kept", []string{"kept"}},
		{"scraped junk survives", "Highlight:music\n  \n    \n  Subscribe", []string{"music\n  \n    \n  Subscribe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hls := parseHighlights(tc.in)
			got := make([]string, len(hls))
			for i, h := range hls {
				got[i] = h.Quote
				if h.Index != i {
					t.Errorf("highlight %d has index %d", i, h.Index)
				}
			}
			if !equalSlices(got, tc.want) {
				t.Errorf("parseHighlights(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- helpers ---------------------------------------------------------------

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
