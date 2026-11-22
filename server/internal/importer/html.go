package importer

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// ErrNotNetscapeHTML means the document is not a bookmark file: it has no <DL>,
// no <DT> and no <A>. An *empty* export is not this error — it has the markup
// and simply contains nothing — so an accidental upload is distinguishable from
// an empty library.
var ErrNotNetscapeHTML = errors.New("importer: not a Netscape bookmark file")

// HTMLReader streams a Netscape bookmark document — the format browsers write
// and phase 12a's export writes.
//
// It is deliberately lenient: real files are missing </DT>, </p> and sometimes
// </DL>, and a browser still reads them. Nothing here can hang, because the
// tokenizer only ever moves forward.
type HTMLReader struct {
	tok *html.Tokenizer

	// folders is the open <DL> stack, innermost last. A bookmark's folder is the
	// non-empty names joined with "/", because nested Space UI does not exist
	// yet (plans/05 test 4).
	folders []string

	// pending is the name of the <H3> that the next <DL> opens.
	pending string

	// title is the text of the anchor or heading being read.
	title strings.Builder
	inA   bool
	inH3  bool

	// cur is the anchor being read; nil between anchors.
	cur *Row

	queue  []Row
	record int

	skipped   int
	warns     []string
	sawMarkup bool
	warnedEnd bool
}

// NewHTMLReader prepares a reader. It never rejects a document here: whether
// there is a bookmark file inside is known only at EOF, and a truncated file
// still yields the bookmarks it holds.
func NewHTMLReader(src io.Reader) (*HTMLReader, error) {
	return &HTMLReader{tok: html.NewTokenizer(src)}, nil
}

// Skipped is how many anchors had no HREF, and so were not bookmarks. The driver
// reports it; silently dropping them would make an import look complete when it
// was not.
func (hr *HTMLReader) Skipped() int { return hr.skipped }

// Warnings are file-level findings — a truncated document, an ADD_DATE that
// will not parse — as opposed to a row's own.
func (hr *HTMLReader) Warnings() []string { return hr.warns }

// Next returns the next bookmark, or io.EOF at the end of the document.
func (hr *HTMLReader) Next() (Row, error) {
	for {
		if len(hr.queue) > 0 {
			row := hr.queue[0]
			hr.queue = hr.queue[1:]
			hr.record++
			row.Record = hr.record
			return row, nil
		}

		tt := hr.tok.Next()
		switch tt {
		case html.ErrorToken:
			if err := hr.tok.Err(); !errors.Is(err, io.EOF) {
				return Row{}, fmt.Errorf("importer: reading bookmark HTML: %w", err)
			}
			// A download cut off mid-anchor still has a URL worth keeping.
			hr.finishAnchor()
			if len(hr.folders) > 0 && !hr.warnedEnd {
				hr.warnedEnd = true
				hr.warn("the document ended with %d folder(s) still open; the bookmarks above it were kept", len(hr.folders))
			}
			if len(hr.queue) > 0 {
				continue
			}
			if !hr.sawMarkup {
				return Row{}, ErrNotNetscapeHTML
			}
			return Row{}, io.EOF

		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := hr.tok.TagName()
			selfClosing := tt == html.SelfClosingTagToken
			switch string(name) {
			case "dl":
				hr.sawMarkup = true
				hr.finishAnchor() // an unclosed </A> ends here, as a browser ends it
				// Real files often omit </H3>, so the heading is closed here if
				// it is still open: the folder name is its text either way.
				if hr.inH3 {
					hr.pending = strings.TrimSpace(hr.title.String())
					hr.inH3 = false
					hr.title.Reset()
				}
				hr.folders = append(hr.folders, hr.pending)
				hr.pending = ""
			case "dt":
				hr.sawMarkup = true
				hr.finishAnchor()
			case "h3":
				hr.sawMarkup = true
				hr.finishAnchor()
				hr.inH3 = true
				hr.title.Reset()
			case "a":
				hr.sawMarkup = true
				// A new anchor closes the previous one even when its </A> is
				// missing, which browsers also do.
				hr.finishAnchor()
				hr.startAnchor()
				if selfClosing {
					hr.finishAnchor()
				}
			}

		case html.EndTagToken:
			name, _ := hr.tok.TagName()
			switch string(name) {
			case "dl":
				if n := len(hr.folders); n > 0 {
					hr.folders = hr.folders[:n-1]
				}
			case "h3":
				hr.pending = strings.TrimSpace(hr.title.String())
				hr.inH3 = false
				hr.title.Reset()
			case "a":
				hr.finishAnchor()
			}

		case html.TextToken:
			if hr.inA || hr.inH3 {
				hr.title.Write(hr.tok.Text())
			}
		}
	}
}

// startAnchor reads the attributes of the anchor the tokenizer is sitting on and
// opens a row for it. An anchor with no HREF is not a bookmark: it is counted
// and never becomes a note.
func (hr *HTMLReader) startAnchor() {
	row := Row{Folder: hr.folder()}

	var (
		href  string
		haveH bool
	)
	for {
		key, val, more := hr.tok.TagAttr()
		switch string(key) {
		case "href":
			href, haveH = string(val), true
		case "add_date":
			created, ok := parseAddDate(string(val))
			switch {
			case !ok:
				row.warn("ADD_DATE %q is not a unix timestamp; the item keeps no original date", string(val))
			default:
				row.Created = created
			}
		case "tags":
			row.Tags = parseTags(string(val))
		}
		if !more {
			break
		}
	}

	if !haveH || strings.TrimSpace(href) == "" {
		// The title text that follows belongs to no item, so it is not
		// collected. Skipped() is what the driver reports.
		hr.skipped++
		return
	}

	row.URL = strings.TrimSpace(href)
	hr.title.Reset()
	hr.inA = true
	hr.cur = &row
}

// finishAnchor closes the anchor being read, if there is one. It is idempotent
// so the tokenizer's missing end tags cannot double a bookmark.
func (hr *HTMLReader) finishAnchor() {
	if hr.cur == nil {
		// title may be a heading's text in progress; leave it alone.
		hr.inA = false
		return
	}
	row := *hr.cur
	row.Title = strings.TrimSpace(hr.title.String())
	hr.cur = nil
	hr.inA = false
	hr.title.Reset()
	hr.queue = append(hr.queue, row)
}

// folder is the current path, innermost last, with unnamed levels dropped.
func (hr *HTMLReader) folder() string {
	var names []string
	for _, name := range hr.folders {
		if name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, "/")
}

func (hr *HTMLReader) warn(format string, args ...any) {
	hr.warns = append(hr.warns, fmt.Sprintf(format, args...))
}

// parseAddDate reads the unix stamp browsers write. Chrome and Firefox both
// write seconds; some tools write milliseconds, which is the same instant.
func parseAddDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, true
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}
	if seconds > 1e12 {
		return time.UnixMilli(seconds).UTC(), true
	}
	return time.Unix(seconds, 0).UTC(), true
}
