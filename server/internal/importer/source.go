// Source detection: which reader does this file want?
//
// The name is no help. Browsers call their export bookmarks.html, Raindrop
// names its CSV a uuid, and a file renamed by hand is still the same file — so
// the first bytes decide, and both callers (the CLI and Settings → Import) ask
// the same question here rather than each carrying their own guess.
package importer

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// sniffBytes is how much of the front of the file decides the format. A
// Netscape doctype and a Raindrop header both sit well inside it.
const sniffBytes = 512

// NewSource picks the reader from the file's contents.
//
// It reads nothing the caller can lose: the bytes it looks at are put back in a
// buffered reader, so the source starts at the beginning of the file.
func NewSource(r io.Reader) (Source, error) {
	buffered := bufio.NewReader(r)
	head, err := buffered.Peek(sniffBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading the file: %w", err)
	}

	trimmed := strings.TrimLeft(string(head), "\ufeff \t\r\n")
	if strings.HasPrefix(trimmed, "<") || strings.Contains(trimmed, "<!DOCTYPE NETSCAPE") {
		return NewHTMLSource(buffered)
	}
	return NewCSVSource(buffered)
}
