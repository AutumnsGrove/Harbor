package httpapi

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/cover"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Issue #49's acceptance list for the proxy: it serves a stored cover, falls
// back silently for everything else, and says `cover: true` only when there is
// something to ask for.

var coverPNG = []byte("\x89PNG\r\n\x1a\nharbor cover bytes")

type stubFetcher struct {
	calls atomic.Int32
	body  []byte
	kind  string
	err   error
}

func (f *stubFetcher) Fetch(context.Context, string) ([]byte, string, error) {
	f.calls.Add(1)
	return f.body, f.kind, f.err
}

// coverHandler builds the full chain with a real cover store over a fake
// fetcher, so the handler test exercises the disk cache as well.
func coverHandler(t *testing.T, fetcher cover.Fetcher) (http.Handler, *store.Store) {
	t.Helper()
	cfg := testConfig(t)
	library, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = library.Close() })

	covers, err := cover.Open(t.TempDir(), fetcher, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("cover.Open: %v", err)
	}

	h := New(cfg, slog.New(slog.DiscardHandler), testAssets(),
		WithStore(library), WithCovers(covers)).Handler()
	return h, library
}

func seedCovered(t *testing.T, library *store.Store, id, ogJSON string) {
	t.Helper()
	if _, err := library.CreateItem(context.Background(), store.ItemInput{
		ID:     id,
		Note:   "x",
		OGJSON: ogJSON,
	}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
}

func TestCoverServesThePageImage(t *testing.T) {
	fetcher := &stubFetcher{body: coverPNG, kind: "image/png"}
	h, library := coverHandler(t, fetcher)
	seedCovered(t, library, "a", `{"image":"https://example.test/og.png"}`)

	rec := request(h, http.MethodGet, "/api/items/a/cover", map[string]string{
		identityHeader: "autumn@grove.place",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("GET cover = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable: the bytes cannot change for this URL", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), coverPNG) {
		t.Errorf("body = %q, want the fetched bytes", rec.Body.Bytes())
	}

	// The second request is a disk read, not a second outgoing fetch.
	again := request(h, http.MethodGet, "/api/items/a/cover", map[string]string{
		identityHeader: "autumn@grove.place",
	})
	if again.Code != http.StatusOK {
		t.Fatalf("second GET cover = %d, want 200", again.Code)
	}
	if fetcher.calls.Load() != 1 {
		t.Errorf("fetches = %d, want 1", fetcher.calls.Load())
	}
}

// The importer writes `cover`; enrichment writes `image`. A card must work for
// either, because 471 rows have only the first.
func TestCoverReadsTheImporterKeyToo(t *testing.T) {
	h, library := coverHandler(t, &stubFetcher{body: coverPNG, kind: "image/png"})
	seedCovered(t, library, "b", `{"cover":"https://example.test/raindrop.jpg","excerpt":"x"}`)

	rec := request(h, http.MethodGet, "/api/items/b/cover", map[string]string{
		identityHeader: "autumn@grove.place",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET cover = %d, want 200 from the importer's key", rec.Code)
	}
}

func TestCoverFallsBackTo404ForEverythingElse(t *testing.T) {
	tests := []struct {
		name   string
		og     string
		seeded bool
		fetch  *stubFetcher
	}{
		{
			name:  "a page with no image",
			og:    `{"excerpt":"just words"}`,
			fetch: &stubFetcher{body: coverPNG, kind: "image/png"},
		},
		{
			name:  "empty og_json",
			og:    "",
			fetch: &stubFetcher{body: coverPNG, kind: "image/png"},
		},
		{
			name:  "og_json that is not JSON",
			og:    "{not json",
			fetch: &stubFetcher{body: coverPNG, kind: "image/png"},
		},
		{
			name:  "a response that is not an image",
			og:    `{"image":"https://example.test/page"}`,
			fetch: &stubFetcher{body: []byte("<html>"), kind: "text/html"},
		},
		{
			name:  "an oversized response",
			og:    `{"image":"https://example.test/huge.png"}`,
			fetch: &stubFetcher{body: make([]byte, cover.MaxBytes+1), kind: "image/png"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, library := coverHandler(t, tc.fetch)
			seedCovered(t, library, "c", tc.og)

			rec := request(h, http.MethodGet, "/api/items/c/cover", map[string]string{
				identityHeader: "autumn@grove.place",
			})
			if rec.Code != http.StatusNotFound {
				t.Fatalf("GET cover = %d, want 404 so the card keeps its placeholder", rec.Code)
			}
		})
	}
}

func TestCoverAnswers404ForAMissingItem(t *testing.T) {
	h, _ := coverHandler(t, &stubFetcher{body: coverPNG, kind: "image/png"})

	rec := request(h, http.MethodGet, "/api/items/nope/cover", map[string]string{
		identityHeader: "autumn@grove.place",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET cover for a missing item = %d, want 404", rec.Code)
	}
}

func TestCoverWithoutAStoreIs404Not500(t *testing.T) {
	// A binary built without a cover store must answer cleanly, the same way a
	// failed fetch does.
	cfg := testConfig(t)
	library, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = library.Close() })
	h := New(cfg, slog.New(slog.DiscardHandler), testAssets(), WithStore(library)).Handler()
	seedCovered(t, library, "d", `{"image":"https://example.test/a.png"}`)

	rec := request(h, http.MethodGet, "/api/items/d/cover", map[string]string{
		identityHeader: "autumn@grove.place",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET cover without a store = %d, want 404", rec.Code)
	}
}

// The card decides from this flag, so it has to be true exactly when there is
// an image URL to ask about.
func TestItemDTOSaysWhetherACoverExists(t *testing.T) {
	h, library := coverHandler(t, &stubFetcher{body: coverPNG, kind: "image/png"})
	seedCovered(t, library, "with", `{"image":"https://example.test/a.png"}`)
	seedCovered(t, library, "without", `{"excerpt":"no image here"}`)

	with := decode(t, request(h, http.MethodGet, "/api/items/with", map[string]string{
		identityHeader: "autumn@grove.place",
	}))["item"].(map[string]any)
	if with["cover"] != true {
		t.Errorf("cover = %v, want true", with["cover"])
	}
	// The URL is never sent: the card asks this origin instead.
	if _, leaked := with["ogImage"]; leaked {
		t.Error("the DTO exposes the third-party image URL")
	}

	without := decode(t, request(h, http.MethodGet, "/api/items/without", map[string]string{
		identityHeader: "autumn@grove.place",
	}))["item"].(map[string]any)
	if without["cover"] != nil {
		t.Errorf("cover = %v, want absent", without["cover"])
	}
}

// A cover response is not the shell, so it must not be compressed or wrapped
// in the HTML policy: it is bytes with its own type.
func TestCoverIsServedAsAnImageNotJSON(t *testing.T) {
	h, library := coverHandler(t, &stubFetcher{body: coverPNG, kind: "image/png"})
	seedCovered(t, library, "e", `{"image":"https://example.test/a.png"}`)

	rec := request(h, http.MethodGet, "/api/items/e/cover", map[string]string{
		identityHeader:    "autumn@grove.place",
		"Accept-Encoding": "gzip",
	})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q: a cover is served untouched", got)
	}
}

var _ cover.Fetcher = (*stubFetcher)(nil)
