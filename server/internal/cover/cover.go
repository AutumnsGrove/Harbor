// Package cover fetches and stores the page image an item's og_json points at.
//
// This is a security boundary. `og:image` is attacker-controlled, so every
// fetch goes through fetch.Client's SSRF guard, and the bytes are only kept if
// the response says it is an image and stays inside the size cap. A failure is
// never fatal: the card falls back to its gradient, and the only trace is one
// debug line (issue #49).
//
// The stored file is named by the hash of the image URL, written atomically and
// served with an immutable cache header. That makes a second request a disk
// read, and makes it impossible for a half-written file to be served.
package cover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AutumnsGrove/Harbor/server/internal/fetch"
)

// MaxBytes bounds what a cover may weigh. It is lower than the page cap: a
// 5 MB hero image is not what a 16:10 card needs, and the potato has 4 GB.
const MaxBytes int64 = 2 << 20

var (
	// ErrNoImage is a response that is not an image, or an empty one.
	ErrNoImage = errors.New("cover: the response is not an image")
	// ErrTooLarge is a response past MaxBytes.
	ErrTooLarge = errors.New("cover: larger than the cap")
)

// Fetcher is the one thing cover needs from the network. fetch.Client satisfies
// it through HTTP, and tests use a fake.
type Fetcher interface {
	// Fetch returns the body and the declared content type. It must apply its
	// own address guard and size cap.
	Fetch(ctx context.Context, rawURL string) ([]byte, string, error)
}

// HTTP adapts fetch.Client: the same guard and timeouts enrichment uses.
type HTTP struct {
	Client *fetch.Client
}

// Fetch retrieves rawURL through the SSRF guard.
func (h HTTP) Fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	page, err := h.Client.Get(ctx, rawURL)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = page.Close() }()

	if page.Size > MaxBytes {
		return nil, "", fmt.Errorf("%w: %d bytes declared", ErrTooLarge, page.Size)
	}
	// One byte past the cap is enough to know it is over.
	body, err := io.ReadAll(io.LimitReader(page.Body, MaxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("cover: reading response: %w", err)
	}
	if int64(len(body)) > MaxBytes {
		return nil, "", fmt.Errorf("%w: more than %d bytes", ErrTooLarge, MaxBytes)
	}
	return body, page.ContentType, nil
}

// Stored is a cover on disk, ready to serve.
type Stored struct {
	Path        string
	ContentType string
	Size        int64
}

// Store owns the cover directory. It is safe for concurrent use and collapses
// concurrent misses for one URL into a single outgoing fetch, so a feed of
// thirty cards from one host makes one request rather than thirty.
type Store struct {
	dir     string
	fetcher Fetcher
	log     *slog.Logger

	mu    sync.Mutex
	locks map[string]*keyLock
}

type keyLock struct {
	mutex sync.Mutex
	refs  int
}

// Open prepares the cover directory. A missing directory is created; a file
// where the directory should be is an error worth failing startup on.
func Open(dir string, fetcher Fetcher, log *slog.Logger) (*Store, error) {
	if fetcher == nil {
		return nil, errors.New("cover: no fetcher")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cover: creating %s: %w", dir, err)
	}
	return &Store{dir: dir, fetcher: fetcher, log: log, locks: map[string]*keyLock{}}, nil
}

// Get returns the stored cover for imageURL, fetching it once on first use.
// Every error is a card that keeps its placeholder: the caller logs and serves
// a 404 rather than retrying.
func (s *Store) Get(ctx context.Context, imageURL string) (Stored, error) {
	imageURL = strings.TrimSpace(imageURL)
	if imageURL == "" {
		return Stored{}, ErrNoImage
	}

	name := Key(imageURL)
	path := filepath.Join(s.dir, name+".bin")

	release := s.lock(name)
	defer release()

	if stored, ok := s.read(path); ok {
		return stored, nil
	}

	body, contentType, err := s.fetcher.Fetch(ctx, imageURL)
	if err != nil {
		return Stored{}, err
	}
	if len(body) == 0 {
		return Stored{}, ErrNoImage
	}
	if int64(len(body)) > MaxBytes {
		return Stored{}, fmt.Errorf("%w: %d bytes", ErrTooLarge, len(body))
	}
	if !isImage(contentType) {
		return Stored{}, fmt.Errorf("%w: %q", ErrNoImage, contentType)
	}

	if err := s.write(path, name, body, contentType); err != nil {
		return Stored{}, err
	}
	return Stored{Path: path, ContentType: mediaType(contentType), Size: int64(len(body))}, nil
}

// read answers from disk when both the bytes and their type are there. The
// sidecar is what keeps a hit from having to sniff or re-request anything.
func (s *Store) read(path string) (Stored, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return Stored{}, false
	}
	contentType, err := os.ReadFile(strings.TrimSuffix(path, ".bin") + ".type")
	if err != nil {
		return Stored{}, false
	}
	served := mediaType(string(contentType))
	if !isImage(served) {
		return Stored{}, false
	}
	return Stored{Path: path, ContentType: served, Size: info.Size()}, true
}

// write puts the bytes and the type in place atomically: a temp file in the
// same directory, then a rename. A concurrent request never sees a partial body.
func (s *Store) write(path, name string, body []byte, contentType string) error {
	tmp, err := os.CreateTemp(s.dir, name+".*.tmp")
	if err != nil {
		return fmt.Errorf("cover: temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("cover: writing: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cover: closing: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("cover: chmod: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("cover: rename: %w", err)
	}

	typePath := strings.TrimSuffix(path, ".bin") + ".type"
	typeTmp := typePath + ".tmp"
	if err := os.WriteFile(typeTmp, []byte(mediaType(contentType)), 0o644); err != nil {
		return fmt.Errorf("cover: writing type: %w", err)
	}
	if err := os.Rename(typeTmp, typePath); err != nil {
		return fmt.Errorf("cover: rename type: %w", err)
	}
	return nil
}

// lock serialises the work for one URL and drops the entry when the last
// waiter leaves, so the map does not grow with the library.
func (s *Store) lock(name string) func() {
	s.mu.Lock()
	entry, ok := s.locks[name]
	if !ok {
		entry = &keyLock{}
		s.locks[name] = entry
	}
	entry.refs++
	s.mu.Unlock()

	entry.mutex.Lock()

	return func() {
		entry.mutex.Unlock()
		s.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(s.locks, name)
		}
		s.mu.Unlock()
	}
}

// Key is the content-addressing of one image URL: the cache file name, the
// ETag, and the version the card puts in its src. A re-enrichment that changes
// the image URL changes all three together, so a cached browser copy cannot
// outlive the bytes it was made from.
func Key(imageURL string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(imageURL)))
	return hex.EncodeToString(sum[:])
}

// mediaType drops the parameters from a content type, so "image/png; charset=x"
// is stored as "image/png".
func mediaType(contentType string) string {
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		return parsed
	}
	if base, _, ok := strings.Cut(contentType, ";"); ok {
		return strings.ToLower(strings.TrimSpace(base))
	}
	return strings.ToLower(strings.TrimSpace(contentType))
}

// isImage is an allowlist of raster formats, not a prefix test. An SVG is an
// image to a browser but a document too: navigated to directly, a script inside
// it runs at this origin. Refusing it costs nothing — og:image is a raster
// almost always — and closes a stored-XSS route through an attacker-chosen URL.
func isImage(contentType string) bool {
	switch mediaType(contentType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif":
		return true
	}
	return false
}
