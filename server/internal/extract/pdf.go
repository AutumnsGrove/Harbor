package extract

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/ledongthuc/pdf"
)

// PDFs are documents Harbor can read without rendering anything: the text
// layer is inside the file, and a document that has none is a scan, which this
// package reports instead of pretending it is empty.
//
// The library is ledongthuc/pdf, chosen by spike S6 (docs/plans/spikes.md): it
// reads a 100-page paper in 1.7 s with a flat 20 MB of RSS, where rsc.io/pdf
// panics on the same file and pdfcpu hands back font-encoded content streams
// with no text in them. Its GetPlainText drops every space — the PDFs that
// matter separate words with kerning offsets, not space glyphs — so the walker
// below uses the positions it already computes.
const (
	// MaxPDFPages bounds one document's work at about 6 s on the potato.
	// A longer file is truncated rather than refused: a 400-page report is
	// still worth its first 150 pages of text.
	MaxPDFPages = 150
	// ScanCharsPerPage is the text-to-page ratio below which a PDF is a scan,
	// once it has ScanPages pages to average over. Measured separation is total:
	// 0 characters per page for an image-only document against 1,106 to 3,201
	// for real text (spike S6), so the threshold's exact value does not matter
	// as long as it sits between them (open-questions J4 says 50).
	ScanCharsPerPage = 50
	// ScanPages is how many pages a short average has to be taken over before
	// it means "no text layer". A one-page flyer with 40 readable characters is
	// a document, not a scan; a 30-page file averaging 40 is a scan. Text that
	// is empty outright is a scan at any length.
	ScanPages = 3
)

// Text states a document can be in after a read. They are recorded per item so
// a badge can say "there is nothing to read in this" without calling it a
// failure (issue #74), and so the OCR path (issue #99) knows what is left.
const (
	TextStateText  = "text"
	TextStateEmpty = "empty"
)

// PDFInfo is what a PDF read adds to a Result. It is nil for every other
// shape, so nothing else in the pipeline has to know about it.
type PDFInfo struct {
	Pages        int
	PagesRead    int
	CharsPerPage float64
	TextState    string
}

// IsPDF reports whether a content type is a PDF.
func IsPDF(contentType string) bool {
	return mediaTypeOf(contentType) == "application/pdf"
}

// LooksLikePDF reports whether bytes start with the PDF magic number. A server
// that sends application/octet-stream for a .pdf (or nothing at all) still
// holds a document, and refusing it would fail a save for the wrong reason.
func LooksLikePDF(head []byte) bool {
	return len(head) >= 5 && string(head[:5]) == "%PDF-"
}

// IsDocumentBody is the predicate both the byte cap and the reader use: the
// label, or failing that the magic number. One rule in one place, so a
// mislabelled document cannot pass one and fail the other — which is exactly
// what review found when the cap was chosen from the content type alone.
func IsDocumentBody(contentType string, head []byte) bool {
	return IsPDF(contentType) || LooksLikePDF(head)
}

// PDF reads one document's text layer. name is the URL's last path segment,
// used as the title when the document carries none — which is most of them.
//
// A document with no text layer is not an error: it comes back with
// TextStateEmpty and no text, because an unreadable scan is still her save
// (issue #74).
func PDF(r io.ReaderAt, size int64, name string) (*Result, error) {
	if r == nil || size <= 0 {
		return nil, errors.New("extract: no input")
	}
	text, info, title, err := readPDF(r, size)
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = titleFromName(name)
	}
	text = truncate(text, MaxTextBytes)
	result := &Result{
		Title:    truncate(title, MaxTitleBytes),
		Text:     text,
		Markdown: text,
		PDF:      info,
	}
	if info.PagesRead > 0 {
		info.CharsPerPage = float64(len(text)) / float64(info.PagesRead)
	}
	info.TextState = TextStateText
	if len(text) == 0 || (info.PagesRead >= ScanPages && info.CharsPerPage < ScanCharsPerPage) {
		info.TextState = TextStateEmpty
		result.Text = ""
		result.Markdown = ""
	}
	return result, nil
}

// readPDF is PDF without the policy, and it is the only place that touches the
// library. A recover is not defensive clutter here: S6 found rsc.io/pdf — a
// sibling of this parser — panicking on a modern arxiv file, and a panic in a
// background worker takes the process with it.
func readPDF(r io.ReaderAt, size int64) (text string, info *PDFInfo, title string, err error) {
	defer func() {
		if p := recover(); p != nil {
			text, info, title = "", nil, ""
			err = fmt.Errorf("extract: reading the PDF panicked: %v", p)
		}
	}()

	doc, err := pdf.NewReader(r, size)
	if err != nil {
		return "", nil, "", fmt.Errorf("extract: opening the PDF: %w", err)
	}
	total := doc.NumPage()
	if total <= 0 {
		return "", nil, "", errors.New("extract: the PDF has no pages")
	}
	read := total
	if read > MaxPDFPages {
		read = MaxPDFPages
	}
	info = &PDFInfo{Pages: total, PagesRead: read}

	var sb strings.Builder
	for i := 1; i <= read; i++ {
		page := doc.Page(i)
		if page.V.IsNull() {
			continue
		}
		if err := appendPageText(&sb, page); err != nil {
			return "", nil, "", fmt.Errorf("extract: page %d: %w", i, err)
		}
		if sb.Len() > MaxTextBytes {
			break
		}
	}
	return tidyText(sb.String()), info, pdfTitle(doc), nil
}

// appendPageText walks one page's glyph runs and rebuilds the white space the
// file does not spell as text. Three things say "a word ended here": a run whose
// own text is a space glyph, a gap wider than a fifth of the font size, and a
// jump in Y. Without the rules, S6's real sample read
// "Providedproperattributionisprovided".
func appendPageText(sb *strings.Builder, page pdf.Page) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("reading the page panicked: %v", p)
		}
	}()
	var prev pdf.Text
	have := false
	for _, t := range page.Content().Text {
		if t.S == "" {
			continue
		}
		blank := strings.TrimSpace(t.S) == ""
		switch {
		case !have:
			if !blank {
				// The first text on a page starts its own line.
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(t.S)
				have = true
			}
		default:
			size := max(t.FontSize, prev.FontSize, 1)
			switch {
			case t.Y-prev.Y > size/2 || prev.Y-t.Y > size/2:
				sb.WriteString("\n")
				if !blank {
					sb.WriteString(t.S)
				}
			case blank, t.X-(prev.X+prev.W) > 0.2*size:
				// A space glyph is ink that means a gap; either way it is one
				// space, which tidyText collapses if both rules fire.
				sb.WriteString(" ")
				if !blank {
					sb.WriteString(t.S)
				}
			default:
				sb.WriteString(t.S)
			}
		}
		prev = t
	}
	return nil
}

// tidyText squeezes the white space the rules above can double up: one space
// between words, one newline between lines, no blank lines. A page that ends up
// empty is a page with no text layer.
func tidyText(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if fields := strings.Join(strings.Fields(line), " "); fields != "" {
			out = append(out, fields)
		}
	}
	return strings.Join(out, "\n")
}

// pdfTitle reads the document's own title, which many PDFs carry in their Info
// dictionary and many others fill with the software that made them.
func pdfTitle(doc *pdf.Reader) string {
	trailer := doc.Trailer()
	if trailer.IsNull() {
		return ""
	}
	info := trailer.Key("Info")
	if info.IsNull() {
		return ""
	}
	title := strings.TrimSpace(info.Key("Title").Text())
	if unusableTitle(title) {
		return ""
	}
	return title
}

// unusableTitle rejects the placeholders generators write: a producer's name, an
// empty string, or something a document did not really title itself.
func unusableTitle(title string) bool {
	if title == "" || len(title) > MaxTitleBytes {
		return true
	}
	switch strings.ToLower(title) {
	case "untitled", "microsoft word", "document", "no title", "title":
		return true
	}
	return strings.Contains(title, "Microsoft® Word")
}

// titleFromName turns a URL's last segment into something readable:
// "attention-is-all-you-need.pdf" is "attention is all you need". A URL with no
// usable last segment yields "" so the item keeps whatever title it had.
func titleFromName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if u, err := url.Parse(name); err == nil && u.Path != "" {
		name = u.Path
	}
	base := path.Base(name)
	if base == "." || base == "/" || base == "" {
		return ""
	}
	if ext := path.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	// A hex blob, an id, or a single short token is not a title; leave the item
	// with whatever it already had. "2504.18412" is an arxiv id, not a name.
	if len(base) < 3 || !hasLetter(base) || isOpaque(base) {
		return ""
	}
	base = strings.NewReplacer("_", " ", "-", " ").Replace(base)
	return strings.Join(strings.Fields(base), " ")
}

// hasLetter reports whether a candidate title is made of more than digits and
// punctuation, which is what a document id looks like.
func hasLetter(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

// isOpaque reports a name that is a hash rather than words: a long run of hex,
// or a long unbroken consonant cluster. Both arrive from a CDN, and neither
// belongs in a title. The hex test ignores the separators, because a hashed name
// arrives hyphenated as often as not ("d6cf572e-3617-4c29…").
func isOpaque(s string) bool {
	lower := strings.ToLower(s)
	core := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, lower)
	if len(core) >= 12 && strings.Trim(core, "0123456789abcdef") == "" {
		return true
	}
	if strings.ContainsAny(lower, " .") {
		return false
	}
	letters := 0
	for _, r := range lower {
		if r >= 'a' && r <= 'z' {
			letters++
		}
	}
	return letters >= 6 && !strings.ContainsAny(lower, "aeiou")
}
