package importer

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/canon"
)

// FuzzImportRow is plans/05 test 3 and plans/testing.md's target for this phase:
// hostile CSV never panics, never ends the run, and never produces a row the
// driver cannot act on. Canonical URL idempotence is asserted per row because a
// row is where a bookmark enters the library.
//
// Seeds live in testdata/fuzz/FuzzImportRow/ and replay as ordinary tests on
// every `go test ./...`.
func FuzzImportRow(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		cr, err := NewCSVReader(bytes.NewReader(data))
		if err != nil {
			if !errors.Is(err, ErrNotRaindropCSV) {
				t.Fatalf("NewCSVReader returned an untyped error: %v", err)
			}
			return
		}

		// Every record consumes at least one byte, so more records than bytes
		// means the reader is not advancing.
		for seen := 0; ; seen++ {
			if seen > len(data)+1 {
				t.Fatalf("reader produced more records than the file has bytes")
			}
			row, err := cr.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				t.Fatalf("Next ended the run on malformed input: %v", err)
			}
			if row.URL == "" && strings.TrimSpace(row.Title) == "" && len(row.Warnings) == 0 {
				t.Fatalf("an empty record was returned without a warning: %+v", row)
			}
			for _, h := range row.Highlights {
				if strings.TrimSpace(h.Quote) == "" {
					t.Fatalf("a highlight with no quote was returned: %+v", h)
				}
			}
			if row.URL == "" {
				continue
			}
			once, err := canon.CanonicalURL(row.URL)
			if err != nil {
				continue // the canonicaliser rejects it; the driver counts that
			}
			twice, err := canon.CanonicalURL(once)
			if err != nil || twice != once {
				t.Fatalf("canonical URL is not idempotent: %q -> %q -> %q (%v)", row.URL, once, twice, err)
			}
		}
	})
}
