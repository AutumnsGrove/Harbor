package importer

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/canon"
)

// readHTML drains a bookmark document the way the driver does.
func readHTML(t *testing.T, src string) ([]Row, *HTMLReader) {
	t.Helper()
	hr, err := NewHTMLReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("NewHTMLReader: %v", err)
	}
	var rows []Row
	for {
		row, err := hr.Next()
		if errors.Is(err, io.EOF) {
			return rows, hr
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		rows = append(rows, row)
	}
}

// readHTMLToEOF drains a document and returns the error that ended it, so a
// truncation can be asserted as a typed error rather than a warning string.
func readHTMLToEOF(t *testing.T, src string) ([]Row, *HTMLReader, error) {
	t.Helper()
	hr, err := NewHTMLReader(strings.NewReader(src))
	if err != nil {
		t.Fatalf("NewHTMLReader: %v", err)
	}
	var rows []Row
	for {
		row, err := hr.Next()
		if errors.Is(err, io.EOF) || errors.Is(err, ErrTruncatedHTML) || errors.Is(err, ErrNotNetscapeHTML) {
			return rows, hr, err
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		rows = append(rows, row)
	}
}

func readHTMLFile(t *testing.T, path string) ([]Row, *HTMLReader) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hr, err := NewHTMLReader(f)
	if err != nil {
		t.Fatalf("NewHTMLReader: %v", err)
	}
	var rows []Row
	for {
		row, err := hr.Next()
		if errors.Is(err, io.EOF) {
			return rows, hr
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		rows = append(rows, row)
	}
}

// Netscape HTML carries URL, title, folder and ADD_DATE, which is what
// design.md §5 says an HTML import preserves. Folders are path-joined because
// nested Space UI does not exist yet (plans/05 test 4).
func TestReadNetscapeNested(t *testing.T) {
	rows, hr := readHTMLFile(t, "testdata/netscape/nested.html")

	got := make([]struct {
		url, title, folder string
	}, len(rows))
	for i, r := range rows {
		got[i].url, got[i].title, got[i].folder = r.URL, r.Title, r.Folder
	}
	want := []struct{ url, title, folder string }{
		{"https://example.com/top", "A & B, top level", ""},
		{"https://example.com/one", "First \u2014 em dash", "Articles"},
		{"https://example.com/deep", "Deep, no date", "Articles/Nested"},
		{"https://example.com/last", "Last", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i+1, got[i], want[i])
		}
	}

	// ADD_DATE is unix seconds, and an absent one is not an error.
	if want := time.Unix(1700000000, 0).UTC(); !rows[0].Created.Equal(want) {
		t.Errorf("row 1 created = %v, want %v", rows[0].Created, want)
	}
	if !rows[2].Created.IsZero() {
		t.Errorf("row 3 created = %v, want zero", rows[2].Created)
	}
	if want := []string{"one", "two"}; !equalSlices(rows[1].Tags, want) {
		t.Errorf("row 2 tags = %v, want %v", rows[1].Tags, want)
	}
	if rows[3].Record != 4 {
		t.Errorf("row 4 record = %d, want 4", rows[3].Record)
	}

	// An anchor with no HREF is not a bookmark; it is counted, not imported and
	// not a note.
	if hr.Skipped() != 1 {
		t.Errorf("skipped = %d, want 1", hr.Skipped())
	}
	if len(hr.Warnings()) != 0 {
		t.Errorf("warnings: %v", hr.Warnings())
	}
}

// Chrome's export adds ICON data URIs, PERSONAL_TOOLBAR_FOLDER, <HR> and <DD>
// descriptions; none of it is a reason to lose a bookmark, and the description
// is deliberately not imported (plans/05 maps URL, title and folder only).
func TestReadNetscapeChrome(t *testing.T) {
	rows, hr := readHTMLFile(t, "testdata/netscape/chrome.html")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Folder != "Bookmarks bar" || rows[0].Title != "A tab" {
		t.Errorf("row 1 = %+v", rows[0])
	}
	// A millisecond ADD_DATE (13 digits) is the same instant as its seconds.
	if want := time.Unix(1710000000, 0).UTC(); !rows[0].Created.Equal(want) {
		t.Errorf("row 1 created = %v, want %v", rows[0].Created, want)
	}
	if rows[1].Folder != "Other" || rows[1].URL != "https://example.com/other" {
		t.Errorf("row 2 = %+v", rows[1])
	}
	if len(rows[1].Note) != 0 {
		t.Errorf("row 2 note = %q, want empty: <DD> is not imported", rows[1].Note)
	}
	if hr.Skipped() != 0 || len(hr.Warnings()) != 0 {
		t.Errorf("skipped = %d, warnings = %v", hr.Skipped(), hr.Warnings())
	}
}

// A file that is not a bookmark document at all is a typed error, so an
// accidental upload does not look like an empty library.
func TestNetscapeNotHTML(t *testing.T) {
	hr, err := NewHTMLReader(strings.NewReader("just some text, no bookmark markup"))
	if err != nil {
		t.Fatalf("NewHTMLReader: %v", err)
	}
	if _, err := hr.Next(); !errors.Is(err, ErrNotNetscapeHTML) {
		t.Errorf("err = %v, want ErrNotNetscapeHTML", err)
	}
}

func TestNetscapeEmptyDocument(t *testing.T) {
	rows, _ := readHTML(t, "<!DOCTYPE NETSCAPE-Bookmark-file-1>\n<DL><p>\n</DL><p>\n")
	if len(rows) != 0 {
		t.Errorf("got %d rows from an empty document", len(rows))
	}
}

// Judging by the files people actually produce, </DT>, </p> and even </DL> go
// missing. Nothing may hang on them.
func TestNetscapeSloppyMarkup(t *testing.T) {
	src := "<DL><p><DT><H3>A folder<DL><p><DT><A HREF=\"https://example.com/a\">A</A><DT><A HREF=\"https://example.com/b\"><b>B</b> in bold</A>"
	rows, hr, err := readHTMLToEOF(t, src)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Folder != "A folder" || rows[1].Folder != "A folder" {
		t.Errorf("folders = %q, %q", rows[0].Folder, rows[1].Folder)
	}
	if rows[1].Title != "B in bold" {
		t.Errorf("title = %q, want the anchor's text content", rows[1].Title)
	}
	if !errors.Is(err, ErrTruncatedHTML) {
		t.Errorf("err = %v, want ErrTruncatedHTML: the document ended inside a folder", err)
	}
	if !strings.Contains(err.Error(), "2 bookmark") {
		t.Errorf("the truncation error should carry the row count: %v", err)
	}
	_ = hr
}

// A truncated download must still yield the bookmarks it contains, and it must
// say that it was cut short (plans/05 "Be lenient, never hang").
func TestNetscapeTruncated(t *testing.T) {
	src := "<DL><p><DT><A HREF=\"https://example.com/a\" ADD_DATE=\"1700000000\">A title that never closes"
	rows, _, err := readHTMLToEOF(t, src)
	if len(rows) != 1 || rows[0].URL != "https://example.com/a" {
		t.Fatalf("rows = %+v, want the truncated anchor's URL", rows)
	}
	if !errors.Is(err, ErrTruncatedHTML) {
		t.Errorf("err = %v, want ErrTruncatedHTML: the document was cut off mid-anchor", err)
	}
}

// A folder with no bookmarks in it, and a bookmark with no folder, are both
// normal; neither may invent a Space name.
func TestNetscapeFoldersAndLooseBookmarks(t *testing.T) {
	src := "<DL><p><DT><H3>Empty</H3><DL><p></DL><p><DT><A HREF=\"https://example.com/loose\">Loose</A></DL><p>"
	rows, _ := readHTML(t, src)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].Folder != "" {
		t.Errorf("folder = %q, want the top level", rows[0].Folder)
	}
}

func TestNetscapeAddDate(t *testing.T) {
	for _, tc := range []struct {
		name, attr string
		want       time.Time
		warn       bool
	}{
		{"seconds", "1700000000", time.Unix(1700000000, 0).UTC(), false},
		{"milliseconds", "1700000000000", time.Unix(1700000000, 0).UTC(), false},
		{"empty", "", time.Time{}, false},
		{"garbage", "yesterday", time.Time{}, true},
		{"negative", "-1", time.Time{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "<DL><p><DT><A HREF=\"https://example.com/a\" ADD_DATE=\"" + tc.attr + "\">A</A></DL><p>"
			rows, _ := readHTML(t, src)
			if len(rows) != 1 {
				t.Fatalf("got %d rows, want 1", len(rows))
			}
			if !rows[0].Created.Equal(tc.want) {
				t.Errorf("created = %v, want %v", rows[0].Created, tc.want)
			}
			if got := hasWarning(rows[0].Warnings, "ADD_DATE"); got != tc.warn {
				t.Errorf("warnings = %v, want a warning about ADD_DATE: %v", rows[0].Warnings, tc.warn)
			}
		})
	}
}

// The export this project writes must be readable by the importer that reads
// it, or the round-trip in plans/05 cannot be tested. The shape below is what
// export/netscape.go emits.
func TestNetscapeReadsOurOwnExportShape(t *testing.T) {
	src := `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<!-- This is an automatically generated file.
     It will be read and overwritten. DO NOT EDIT! -->
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><A HREF="https://example.com/loose" ADD_DATE="1700000000">Loose one</A>
    <DT><H3>Read later</H3>
    <DL><p>
        <DT><A HREF="https://example.com/filed" ADD_DATE="1700000000" TAGS="rust,reading">Filed &amp; escaped</A>
    </DL><p>
</DL><p>
`
	rows, _ := readHTML(t, src)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Folder != "" || rows[1].Folder != "Read later" {
		t.Errorf("folders = %q, %q", rows[0].Folder, rows[1].Folder)
	}
	if rows[1].Title != "Filed & escaped" {
		t.Errorf("title = %q, want the entity decoded", rows[1].Title)
	}
	if want := []string{"rust", "reading"}; !equalSlices(rows[1].Tags, want) {
		t.Errorf("tags = %v, want %v", rows[1].Tags, want)
	}
}

// Firefox and Chrome write a description as <DD> after the bookmark. In the
// sloppy files this reader supports (</A> missing) the description would
// otherwise be collected as part of the title, and descriptions are not
// imported at all.
func TestNetscapeDDEndsTheAnchor(t *testing.T) {
	src := "<DL><p><DT><A HREF=\"https://example.com/a\">A title<DD>A description that is not imported<DT><A HREF=\"https://example.com/b\">B</A></DL><p>"
	rows, _ := readHTML(t, src)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Title != "A title" {
		t.Errorf("title = %q, want just the anchor's text", rows[0].Title)
	}
	if rows[1].Title != "B" || rows[1].URL != "https://example.com/b" {
		t.Errorf("row 2 = %+v", rows[1])
	}
}

// Nesting is bounded: a document with thousands of unclosed <DL>s costs neither
// quadratic memory nor a quadratic path build, and its bookmarks still land.
func TestNetscapeDeepNestingIsLinear(t *testing.T) {
	var b strings.Builder
	const depth = 2000
	for i := 0; i < depth; i++ {
		b.WriteString("<DT><H3>f</H3><DL><p>")
	}
	b.WriteString("<DT><A HREF=\"https://example.com/deep\">Deep</A>")
	rows, _, _ := readHTMLToEOF(t, b.String())
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if got := strings.Count(rows[0].Folder, "/") + 1; got != maxFolderDepth {
		t.Errorf("folder depth = %d, want it capped at %d", got, maxFolderDepth)
	}
}

// A cut-off anchor with no open folder must still be a truncation: the row the
// EOF pass queues is returned first, so the flag has to be latched rather than
// derived from state finishAnchor() clears.
func TestNetscapeTruncatedLooseAnchor(t *testing.T) {
	for _, src := range []string{
		"<A HREF=\"https://example.com/a\">cut off",
		"<DL><p><DT><A HREF=\"https://example.com/a\">cut off</DL><p>",
	} {
		rows, _, err := readHTMLToEOF(t, src)
		if len(rows) != 1 || rows[0].URL != "https://example.com/a" {
			t.Fatalf("%q: rows = %+v, want the cut-off anchor's URL", src, rows)
		}
		if !errors.Is(err, ErrTruncatedHTML) {
			t.Errorf("%q: err = %v, want ErrTruncatedHTML", src, err)
		}
	}
}

// The files are hostile input like the CSV is, so the same guarantee applies:
// no panic, no hang, no aborted run.
func FuzzImportHTML(f *testing.F) {
	f.Add([]byte("<DL><p><DT><A HREF=\"https://example.com/a\" ADD_DATE=\"1700000000\">A</A></DL><p>"))
	f.Add([]byte("<DT><H3>f<DL><p><DT><A HREF=>x</A>"))
	f.Add([]byte("<!DOCTYPE NETSCAPE-Bookmark-file-1><DL>"))
	f.Add([]byte("<DL><p><DT><A HREF=\"javascript:alert(1)\">x</A></DL>"))
	f.Add([]byte(""))

	f.Fuzz(func(t *testing.T, data []byte) {
		hr, err := NewHTMLReader(bytes.NewReader(data))
		if err != nil {
			return
		}
		for seen := 0; ; seen++ {
			if seen > len(data)+1 {
				t.Fatalf("reader produced more rows than the file has bytes")
			}
			row, err := hr.Next()
			if errors.Is(err, io.EOF) || errors.Is(err, ErrNotNetscapeHTML) || errors.Is(err, ErrTruncatedHTML) {
				return
			}
			if err != nil {
				t.Fatalf("Next ended the run on hostile input: %v", err)
			}
			if row.URL != "" {
				once, err := canon.CanonicalURL(row.URL)
				if err != nil {
					continue
				}
				twice, err := canon.CanonicalURL(once)
				if err != nil || twice != once {
					t.Fatalf("canonical URL is not idempotent: %q -> %q -> %q (%v)", row.URL, once, twice, err)
				}
			}
		}
	})
}
