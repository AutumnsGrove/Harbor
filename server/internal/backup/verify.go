// Checking a backup: decompress it and ask SQLite whether the file is whole.
// This is the difference between a file called a backup and one that is.
package backup

import (
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Verify decompresses one backup and runs PRAGMA integrity_check on it. It is
// what Prune consults before deleting anything and what `harbor backup --verify`
// reports.
func Verify(path string) error {
	restored, cleanup, err := decompressToTemp(path)
	if err != nil {
		return err
	}
	defer cleanup()
	return store.Verify(restored)
}

// Check is one backup's verification.
type Check struct {
	Entry Entry
	Err   error
}

// VerifyAll checks every backup in dir, newest first.
func VerifyAll(dir string) ([]Check, error) {
	entries, err := List(dir)
	if err != nil {
		return nil, err
	}
	checks := make([]Check, 0, len(entries))
	for _, entry := range entries {
		checks = append(checks, Check{Entry: entry, Err: Verify(entry.Path)})
	}
	return checks, nil
}

// decompressToTemp writes the backup's contents to a temporary database file
// beside the system temp directory and returns its path and a cleanup.
func decompressToTemp(path string) (string, func(), error) {
	in, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("backup: opening %s: %w", path, err)
	}
	defer in.Close()

	decoder, err := zstd.NewReader(in)
	if err != nil {
		return "", nil, fmt.Errorf("backup: %s is not a zstd stream: %w", path, err)
	}
	defer decoder.Close()

	out, err := os.CreateTemp("", "harbor-verify-*.db")
	if err != nil {
		return "", nil, fmt.Errorf("backup: creating a verification file: %w", err)
	}
	cleanup := func() { _ = os.Remove(out.Name()) }
	written, err := io.Copy(out, decoder)
	if err != nil {
		_ = out.Close()
		cleanup()
		return "", nil, fmt.Errorf("backup: decompressing %s: %w", path, err)
	}
	if err := out.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("backup: closing the verification file: %w", err)
	}
	// Zero bytes is not an empty database, it is an empty file — and SQLite
	// happily reports integrity_check "ok" for one. So does an empty zstd
	// stream, which io.Copy copies without complaint.
	if written == 0 {
		cleanup()
		return "", nil, fmt.Errorf("backup: %s decompresses to nothing", path)
	}
	// A real database starts with SQLite's header. This catches a file that is
	// a valid zstd stream of something else, which integrity_check cannot.
	head := make([]byte, len(sqliteHeader))
	file, err := os.Open(out.Name())
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("backup: reading back %s: %w", path, err)
	}
	read, err := io.ReadFull(file, head)
	_ = file.Close()
	if err != nil || string(head[:read]) != sqliteHeader {
		cleanup()
		return "", nil, fmt.Errorf("backup: %s is not a SQLite database", path)
	}
	return out.Name(), cleanup, nil
}

// sqliteHeader is the first 16 bytes of every SQLite file.
const sqliteHeader = "SQLite format 3\x00"
