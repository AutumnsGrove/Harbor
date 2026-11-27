// Package media is the content-addressed store behind every image and document
// Harbor keeps: one file per SHA-256, written once, never mutated, addressed by
// its own bytes (design.md §11, plans/06-media.md).
//
// The discipline the rest of the system depends on:
//
//   - the hash is computed while the bytes stream, so nothing is buffered and
//     a 25 MB file costs a 32 KB copy buffer;
//   - a file that already exists is reused rather than rewritten, which is what
//     makes "the same photo from Telegram and from the composer is one file" a
//     structural fact rather than a promise;
//   - the path is derived from the hash alone, so a filename can never reach the
//     filesystem and traversal is impossible rather than filtered;
//   - an original is written temp → fsync → rename, so a crash leaves either no
//     file or the whole file.
//
// Thumbnails are derived and live beside the originals; they are not addressed
// by content and can always be regenerated (spike S12).
package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

var (
	// ErrTooLarge is returned when a blob exceeds the cap it was given.
	ErrTooLarge = errors.New("media: blob is larger than the cap")
	// ErrBadHash is a SHA-256 that is not 64 lowercase hex characters. Nothing
	// else is ever turned into a path.
	ErrBadHash = errors.New("media: not a sha256")
)

// MaxBlobBytes is the ceiling for one blob, matching the Worker's upload cap
// (design.md §4): a file the Worker would refuse should not be accepted here
// either, or the two paths would store different libraries.
const MaxBlobBytes int64 = 25 << 20

// copyBuffer is the streaming chunk size. It is the reason a 25 MB file does not
// cost 25 MB of memory.
const copyBuffer = 32 << 10

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Blob is one stored file.
type Blob struct {
	// SHA is the lowercase hex SHA-256 of the bytes: the identity of the file.
	SHA string
	// Bytes is the size of the blob.
	Bytes int64
	// Path is relative to the store's root, "ab/abcdef…", and is what a media
	// row records.
	Path string
	// Existed reports that these bytes were already stored, so nothing was
	// written. A caller uses it to skip a model call, not just a copy.
	Existed bool
}

// Store is a directory of blobs. It is safe for concurrent use: two writers of
// the same bytes race only on the rename, and both end with the same file.
type Store struct {
	root string
	max  int64
	// rename is os.Rename, and is a field only so the failure branch below can
	// be tested: a rename that fails while the blob is present must count as
	// success, and no filesystem makes that easy to arrange on purpose.
	rename func(oldpath, newpath string) error
}

// New returns a store rooted at dir, creating it if it does not exist.
func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("media: no directory")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	// 0o750: the directory holds her photographs, and the container runs as one
	// uid that owns it.
	if err := os.MkdirAll(root, 0o750); err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	return &Store{root: root, max: MaxBlobBytes, rename: os.Rename}, nil
}

// Root is the absolute directory the store lives in.
func (s *Store) Root() string { return s.root }

// WithMaxBytes changes the per-blob cap.
func (s *Store) WithMaxBytes(n int64) *Store {
	if n > 0 {
		s.max = n
	}
	return s
}

// Put streams r into the store and returns where it landed. The hash is
// computed over the same pass that writes the temp file, so the cap and the
// identity are known without ever holding the blob.
//
// A blob that is already stored is not rewritten: the temp file is removed and
// Blob.Existed says so.
func (s *Store) Put(r io.Reader) (*Blob, error) {
	if r == nil {
		return nil, errors.New("media: no input")
	}
	incoming := filepath.Join(s.root, ".incoming")
	if err := os.MkdirAll(incoming, 0o750); err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	tmp, err := os.CreateTemp(incoming, "blob-*")
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	tmpName := tmp.Name()
	// Every failure path removes the temp file; the success path has already
	// renamed it, and removing a missing name is not an error worth reporting.
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	hash := sha256.New()
	// One byte past the cap distinguishes "at the cap" from "over it".
	written, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(r, s.max+1))
	if err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	if written > s.max {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, s.max)
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	rel := pathFor(sum)
	abs := filepath.Join(s.root, filepath.FromSlash(rel))

	if info, err := os.Stat(abs); err == nil {
		// A directory or a device at a blob path is not this blob: reusing it
		// would hand back something that is not the bytes asked for.
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("media: %s exists and is not a regular file (%s)", sum, info.Mode().Type())
		}
		if info.Size() != written {
			// Same bytes cannot have two sizes. If it happens, the file on disk
			// is damaged and reusing it would hand back wrong bytes under the
			// right name.
			return nil, fmt.Errorf("media: %s exists with %d bytes, expected %d", sum, info.Size(), written)
		}
		return &Blob{SHA: sum, Bytes: written, Path: rel, Existed: true}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("media: %w", err)
	}

	// fsync before the rename: a crash must not leave a renamed file whose
	// contents are still in the page cache.
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, fmt.Errorf("media: %w", err)
	}
	if err := s.rename(tmpName, abs); err != nil {
		// Losing a race with an identical writer is a success: the bytes are
		// there, and they are the same bytes.
		if _, statErr := os.Stat(abs); statErr == nil {
			return &Blob{SHA: sum, Bytes: written, Path: rel, Existed: true}, nil
		}
		return nil, fmt.Errorf("media: %w", err)
	}
	// fsync the directory so the rename itself survives a crash.
	if dir, err := os.Open(filepath.Dir(abs)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return &Blob{SHA: sum, Bytes: written, Path: rel}, nil
}

// Open returns the stored bytes for a hash.
func (s *Store) Open(sha string) (io.ReadCloser, error) {
	abs, err := s.abs(sha)
	if err != nil {
		return nil, err
	}
	return os.Open(abs)
}

// Has reports whether the bytes for a hash are stored.
func (s *Store) Has(sha string) (bool, error) {
	abs, err := s.abs(sha)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(abs); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, fmt.Errorf("media: %w", err)
	}
}

// Abs turns a hash into an absolute path, without touching the filesystem.
func (s *Store) Abs(sha string) (string, error) { return s.abs(sha) }

func (s *Store) abs(sha string) (string, error) {
	if !hashPattern.MatchString(sha) {
		return "", fmt.Errorf("%w: %q", ErrBadHash, sha)
	}
	return filepath.Join(s.root, filepath.FromSlash(pathFor(sha))), nil
}

// pathFor is the only function that turns an identity into a path: two hex
// characters of fan-out and then the hash itself. The value is always safe
// because the caller has already checked it is 64 hex characters.
func pathFor(sha string) string {
	return sha[:2] + "/" + sha
}

// IsHash reports whether s is a well-formed content address.
func IsHash(s string) bool { return hashPattern.MatchString(s) }
