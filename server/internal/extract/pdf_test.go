package extract

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// The fixtures below are written by hand rather than checked in as binaries:
// what they have to prove is this package's own gap-and-line rule, and a
// generated page can put a glyph exactly where the rule needs one. They are
// real PDFs — every parser involved reads them as such — with a base-14 font
// and an explicit /Widths array so positions can be computed.

// pdfFixture assembles a one-font, one-page-per-content-stream PDF.
func pdfFixture(t *testing.T, content ...string) []byte {
	t.Helper()

	widths := make([]string, 0, 95)
	for range 95 { // codes 32..126, every glyph 500/1000 em
		widths = append(widths, "500")
	}
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 /Widths [" +
		strings.Join(widths, " ") + "] >>"

	var bodies []string
	// 1 catalog, 2 pages, then per page: a page dict and its content stream,
	// then the font last so the page dicts can be written before the id is known.
	pageIDs := make([]int, len(content))
	pageDicts := make([]string, len(content))
	for i := range content {
		pageIDs[i] = 3 + 2*i // a page dict and its content stream, two objects each
	}
	fontID := 3 + 2*len(content)
	for i, stream := range content {
		pageDicts[i] = fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
			"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", fontID, pageIDs[i]+1)
		bodies = append(bodies, pageDicts[i],
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	bodies = append(bodies, font)

	kids := make([]string, len(content))
	for i := range content {
		kids[i] = fmt.Sprintf("%d 0 R", pageIDs[i])
	}
	pages := fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(content), strings.Join(kids, " "))
	all := append([]string{"<< /Type /Catalog /Pages 2 0 R >>", pages}, bodies...)

	var buf bytes.Buffer
	offsets := make([]int, len(all)+1)
	buf.WriteString("%PDF-1.5\n%\xe2\xe3\xcf\xd3\n")
	for i, body := range all {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(all)+1)
	for i := 1; i <= len(all); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(all)+1, xref)
	return buf.Bytes()
}

func readPDFString(t *testing.T, body []byte, name string) *Result {
	t.Helper()
	result, err := PDF(bytes.NewReader(body), int64(len(body)), name)
	if err != nil {
		t.Fatalf("PDF: %v", err)
	}
	return result
}

// TestPDFRebuildsSpacesAndLines is the S6 finding as a test: these PDFs separate
// words with positioning, not with space glyphs, so the reader has to put the
// gaps back. Without it the text arrives as "Hellothedocument".
func TestPDFRebuildsSpacesAndLines(t *testing.T) {
	// Three runs: two words separated by a positioning offset (a TJ array, the
	// shape that loses spaces), then a new line by a Y jump.
	// The padding is not decoration: a page under ScanCharsPerPage really is
	// indistinguishable from a scan, so a realistic page needs realistic text.
	page := "BT /F1 12 Tf 72 720 Td [(Hello) -600 (world)] TJ " +
		"0 -20 Td (Second line of a page that carries enough text to be read as a page) Tj ET"
	result := readPDFString(t, pdfFixture(t, page), "harbor-spike-notes.pdf")

	if !strings.Contains(result.Text, "Hello world") {
		t.Errorf("the positioning gap did not become a space: %q", result.Text)
	}
	if !strings.Contains(result.Text, "\nSecond line") {
		t.Errorf("the Y jump did not become a new line: %q", result.Text)
	}
	if got := strings.Count(result.Text, "\n"); got != 1 {
		t.Errorf("newlines = %d, want 1: %q", got, result.Text)
	}
	if result.PDF == nil {
		t.Fatal("a PDF result must carry Document info")
	}
	if result.PDF.Pages != 1 || result.PDF.PagesRead != 1 {
		t.Errorf("pages = %d/%d, want 1/1", result.PDF.PagesRead, result.PDF.Pages)
	}
	if result.PDF.TextState != TextStateText {
		t.Errorf("text state = %q, want %q", result.PDF.TextState, TextStateText)
	}
}

// TestPDFTitleFallsBackToTheFileName covers the common case: no /Info /Title, so
// the item is titled from the URL's last segment rather than left as a bare URL.
func TestPDFTitleFallsBackToTheFileName(t *testing.T) {
	page := "BT /F1 12 Tf 72 720 Td (text that is long enough to count as a page of text) Tj ET"
	result := readPDFString(t, pdfFixture(t, page), "attention-is-all-you-need.pdf")
	if result.Title != "attention is all you need" {
		t.Errorf("title = %q", result.Title)
	}
	// A hashed name is not a title; leave the item's own title alone.
	opaque := readPDFString(t, pdfFixture(t, page), "41df8f28d4ef43e9aed2823f9393e470.pdf")
	if opaque.Title != "" {
		t.Errorf("an opaque file name became the title: %q", opaque.Title)
	}
	// Nor is an id: an arxiv URL's last segment is "2504.18412", and a title of
	// "2504.18" is worse than no title at all.
	for _, name := range []string{"2504.18412.pdf", "1706.03762", "d6cf572e-3617-4c29.pdf"} {
		if got := readPDFString(t, pdfFixture(t, page), name).Title; got != "" {
			t.Errorf("%q became the title", got)
		}
	}
}

// TestPDFScanIsEmptyNotAnError is issue #74's second half: a document with no
// text layer is a scan, which is reported rather than failed — and it is not
// the same thing as a malformed file.
func TestPDFScanIsEmptyNotAnError(t *testing.T) {
	// A page that draws nothing: the shape of a scanned page as far as a text
	// layer is concerned.
	result := readPDFString(t, pdfFixture(t, "q 612 0 0 792 0 0 cm Q"), "scan.pdf")
	if result.PDF.TextState != TextStateEmpty {
		t.Errorf("text state = %q, want %q", result.PDF.TextState, TextStateEmpty)
	}
	if result.Text != "" || result.Markdown != "" {
		t.Errorf("a scan carried text: %q", result.Text)
	}
	if result.Title != "scan" {
		t.Errorf("a scan lost its title: %q", result.Title)
	}
}

// TestPDFPageCapTruncates is the bound that keeps a 400-page report from holding
// a background worker for a minute: it is read, not refused.
func TestPDFPageCapTruncates(t *testing.T) {
	page := "BT /F1 12 Tf 72 720 Td (page text long enough to look like text on this page) Tj ET"
	pages := make([]string, MaxPDFPages+5)
	for i := range pages {
		pages[i] = page
	}
	result := readPDFString(t, pdfFixture(t, pages...), "long.pdf")
	if result.PDF.Pages != MaxPDFPages+5 {
		t.Errorf("total pages = %d, want %d", result.PDF.Pages, MaxPDFPages+5)
	}
	if result.PDF.PagesRead != MaxPDFPages {
		t.Errorf("pages read = %d, want the cap %d", result.PDF.PagesRead, MaxPDFPages)
	}
}

func TestPDFRefusesWhatIsNotAPDF(t *testing.T) {
	for name, body := range map[string][]byte{
		"html":      []byte("<html><body>not a pdf</body></html>"),
		"truncated": []byte("%PDF-1.5\n1 0 obj\n<< /Type /Catalog"),
		"empty":     {},
	} {
		if _, err := PDF(bytes.NewReader(body), int64(len(body)), "x.pdf"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := PDF(nil, 0, "x.pdf"); err == nil {
		t.Error("a nil reader must be an error")
	}
}

func TestIsPDFAndLooksLikePDF(t *testing.T) {
	for _, ct := range []string{"application/pdf", "application/pdf; charset=binary", "APPLICATION/PDF"} {
		if !IsPDF(ct) {
			t.Errorf("IsPDF(%q) = false", ct)
		}
	}
	for _, ct := range []string{"text/html", "image/png", "application/octet-stream", ""} {
		if IsPDF(ct) {
			t.Errorf("IsPDF(%q) = true", ct)
		}
	}
	// A server that mislabels a PDF as octet-stream still hands over bytes that
	// start with the magic number, and that is the decisive test.
	if !LooksLikePDF([]byte("%PDF-1.7\n...")) {
		t.Error("LooksLikePDF missed the magic number")
	}
	for _, head := range []string{"", "%PD", "<html>", "\x00\x01\x02\x03\x04"} {
		if LooksLikePDF([]byte(head)) {
			t.Errorf("LooksLikePDF(%q) = true", head)
		}
	}
}

// FuzzPDFKey keeps the reader's promise that no input panics: a crash in a
// background worker takes the process with it (spike S6 found a sibling library
// that panics on a real file).
func FuzzPDF(f *testing.F) {
	f.Add([]byte("%PDF-1.5\n%\xe2\xe3\xcf\xd3\ntrailer\n<< /Size 0 >>\n%%EOF\n"))
	f.Add([]byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, body []byte) {
		result, err := PDF(bytes.NewReader(body), int64(len(body)), "fuzz.pdf")
		if err != nil {
			return
		}
		if result == nil || result.PDF == nil {
			t.Fatal("a successful read must carry Document info")
		}
		if result.PDF.PagesRead > MaxPDFPages {
			t.Errorf("read %d pages, past the cap %d", result.PDF.PagesRead, MaxPDFPages)
		}
		if len(result.Text) > MaxTextBytes {
			t.Errorf("text is %d bytes, past the cap %d", len(result.Text), MaxTextBytes)
		}
	})
}
