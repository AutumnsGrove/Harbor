package importer

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// The format comes from the bytes, never the name: browsers call their export
// bookmarks.html, Raindrop names its CSV a uuid, and a renamed export is still
// an export.

func TestNewSourceDetectsTheFormat(t *testing.T) {
	tests := []struct {
		name string
		body string
		csv  bool
	}{
		{"a Raindrop header", header + "1,t,,,https://example.com/a,,,2024-01-02T03:04:05Z,,,false\n", true},
		{"a header with a BOM", "\ufeff" + header, true},
		{"a header after blank lines", "\n\n" + header, true},
		{"a Netscape document", "<!DOCTYPE NETSCAPE-Bookmark-file-1>\n<DL><p></DL><p>\n", false},
		{"markup after whitespace", "  \n\t<html><body></body></html>", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src, err := NewSource(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("NewSource: %v", err)
			}
			_, isCSV := src.(*CSVReader)
			if isCSV != tc.csv {
				t.Errorf("detected CSV = %v, want %v", isCSV, tc.csv)
			}
		})
	}
}

// The sniffed bytes are put back: the source reads the file from its first
// byte, header included, so the first row a caller sees is the first row.
func TestNewSourceDoesNotSwallowTheHeader(t *testing.T) {
	src, err := NewSource(strings.NewReader("\ufeff" + header + "1,One,,,https://example.com/one,,,2024-01-02T03:04:05Z,,,false\n"))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	row, err := src.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if row.URL != "https://example.com/one" {
		t.Errorf("first row URL = %q, want the file's first record", row.URL)
	}
}

// A file too short to fill the sniff buffer is a whole file, not an error.
func TestNewSourceReadsAShortFile(t *testing.T) {
	src, err := NewSource(strings.NewReader("url\n"))
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	if _, ok := src.(*CSVReader); !ok {
		t.Errorf("a two-column short file was not read as CSV")
	}
}

// Something that is neither is refused by the reader's own typed error, which
// is what the API turns into a 400 with a sentence a human can act on.
func TestNewSourceRefusesSomethingElse(t *testing.T) {
	_, err := NewSource(strings.NewReader("this is a shopping list\n"))
	if !errors.Is(err, ErrNotRaindropCSV) {
		t.Fatalf("NewSource error = %v, want ErrNotRaindropCSV", err)
	}
}

// The real fixture decides the format the way a real export would.
func TestNewSourceReadsTheRealShapeFixture(t *testing.T) {
	file, err := os.Open("testdata/raindrop/real-shape.csv")
	if err != nil {
		t.Fatalf("opening the fixture: %v", err)
	}
	defer file.Close()

	src, err := NewSource(file)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	if _, ok := src.(*CSVReader); !ok {
		t.Error("the real-shape fixture was not read as CSV")
	}
}
