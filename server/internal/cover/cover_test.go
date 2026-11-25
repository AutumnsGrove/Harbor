package cover

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #49's proxy: one fetch per URL, a disk hit afterwards, and a silent
// refusal for anything that is not an image or is too big.

type fakeFetcher struct {
	calls   atomic.Int32
	body    []byte
	kind    string
	err     error
	release chan struct{} // when non-nil, Fetch waits for it (single-flight test)
}

func (f *fakeFetcher) Fetch(context.Context, string) ([]byte, string, error) {
	f.calls.Add(1)
	if f.release != nil {
		<-f.release
	}
	return f.body, f.kind, f.err
}

func newStore(t *testing.T, fetcher Fetcher) *Store {
	t.Helper()
	store, err := Open(t.TempDir(), fetcher, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

var pngBytes = []byte("\x89PNG\r\n\x1a\n" + "not really a png, but the type header is what matters")

func TestGetFetchesOnceThenReadsFromDisk(t *testing.T) {
	fetcher := &fakeFetcher{body: pngBytes, kind: "image/png; charset=binary"}
	store := newStore(t, fetcher)

	first, err := store.Get(context.Background(), "https://example.test/cover.png")
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if first.ContentType != "image/png" {
		t.Errorf("ContentType = %q, want image/png", first.ContentType)
	}
	if fetcher.calls.Load() != 1 {
		t.Fatalf("fetches = %d, want 1", fetcher.calls.Load())
	}
	onDisk, err := os.ReadFile(first.Path)
	if err != nil || len(onDisk) != len(pngBytes) {
		t.Fatalf("stored file = %d bytes (%v), want %d", len(onDisk), err, len(pngBytes))
	}

	// The second load is a disk read: no second fetch, same bytes.
	second, err := store.Get(context.Background(), "https://example.test/cover.png")
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if second.Path != first.Path {
		t.Errorf("second path = %q, want %q", second.Path, first.Path)
	}
	if fetcher.calls.Load() != 1 {
		t.Errorf("fetches = %d after a cache hit, want 1", fetcher.calls.Load())
	}
}

func TestGetRefusesSVG(t *testing.T) {
	// An SVG can carry script and run it when navigated to at this origin.
	fetcher := &fakeFetcher{body: []byte("<svg onload=\"alert(1)\"/>"), kind: "image/svg+xml"}
	store := newStore(t, fetcher)

	if _, err := store.Get(context.Background(), "https://example.test/logo.svg"); !errors.Is(err, ErrNoImage) {
		t.Fatalf("err = %v, want ErrNoImage for image/svg+xml", err)
	}
}

func TestGetRefusesWhatIsNotAnImage(t *testing.T) {
	fetcher := &fakeFetcher{body: []byte("<html>no</html>"), kind: "text/html; charset=utf-8"}
	store := newStore(t, fetcher)

	_, err := store.Get(context.Background(), "https://example.test/page")
	if !errors.Is(err, ErrNoImage) {
		t.Fatalf("err = %v, want ErrNoImage", err)
	}
	// Nothing was written, so the next request tries again rather than caching
	// the refusal forever.
	entries, _ := os.ReadDir(store.dir)
	if len(entries) != 0 {
		t.Errorf("cover dir holds %d entries, want none", len(entries))
	}
}

func TestGetRefusesOversizedBodies(t *testing.T) {
	fetcher := &fakeFetcher{body: make([]byte, MaxBytes+1), kind: "image/jpeg"}
	store := newStore(t, fetcher)

	_, err := store.Get(context.Background(), "https://example.test/huge.jpg")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestGetPassesTheGuardErrorThrough(t *testing.T) {
	blocked := errors.New("fetch: address is not allowed")
	store := newStore(t, &fakeFetcher{err: blocked})

	_, err := store.Get(context.Background(), "http://127.0.0.1/secret.png")
	if !errors.Is(err, blocked) {
		t.Fatalf("err = %v, want the fetcher's error", err)
	}
}

func TestGetRefusesAnEmptyURL(t *testing.T) {
	store := newStore(t, &fakeFetcher{body: pngBytes, kind: "image/png"})
	if _, err := store.Get(context.Background(), "   "); !errors.Is(err, ErrNoImage) {
		t.Fatalf("err = %v, want ErrNoImage", err)
	}
}

// Thirty cards pointing at one host must not be thirty outgoing fetches.
func TestConcurrentGetsShareOneFetch(t *testing.T) {
	fetcher := &fakeFetcher{body: pngBytes, kind: "image/png", release: make(chan struct{})}
	store := newStore(t, fetcher)

	const waiters = 8
	var group sync.WaitGroup
	group.Add(waiters)
	results := make([]error, waiters)
	for i := 0; i < waiters; i++ {
		go func(i int) {
			defer group.Done()
			_, results[i] = store.Get(context.Background(), "https://example.test/one.png")
		}(i)
	}

	// Let the first goroutine reach the fetch, then release everyone.
	time.Sleep(50 * time.Millisecond)
	close(fetcher.release)
	group.Wait()

	for i, err := range results {
		if err != nil {
			t.Fatalf("waiter %d: %v", i, err)
		}
	}
	if got := fetcher.calls.Load(); got != 1 {
		t.Errorf("fetches = %d, want 1: the per-URL lock did not collapse the misses", got)
	}
}

// A half-written file must never be served: write renames into place.
func TestWriteIsAtomic(t *testing.T) {
	store := newStore(t, &fakeFetcher{body: pngBytes, kind: "image/png"})

	if err := store.write(store.dir+"/x.bin", "x", pngBytes, "image/png"); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if len(entry.Name()) > 4 && entry.Name()[len(entry.Name())-4:] == ".tmp" {
			t.Errorf("temp file %q left behind", entry.Name())
		}
	}
}

// The cache reader trusts its sidecar only if the sidecar says image/*.
func TestReadIgnoresASidecarThatIsNotAnImage(t *testing.T) {
	store := newStore(t, &fakeFetcher{body: pngBytes, kind: "image/png"})
	path := store.dir + "/y.bin"
	if err := os.WriteFile(path, pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.dir+"/y.type", []byte("text/html"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.read(path); ok {
		t.Error("read served a cover whose stored type is text/html")
	}
}
