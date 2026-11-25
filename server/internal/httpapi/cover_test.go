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
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Issue #49's acceptance list for the proxy: it serves a stored cover, falls
// back silently for everything else, and says `cover: true` only when there is
// something to ask for.

var coverPNG = []byte("\x89PNG\r\n\x1a\nharbor cover bytes")

type stubFetcher struct {
	calls atomic.Int32
	kind  string
	body  []byte
	err   error

	// asked is the URL the handler actually requested. It is the only way a test
	// can tell a resolved image from an unresolved one, because a stub returns
	// its body whatever it is given.
	asked string
}

func (f *stubFetcher) Fetch(_ context.Context, rawURL string) ([]byte, string, error) {
	f.calls.Add(1)
	f.asked = rawURL
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
	version, ok := with["cover"].(string)
	if !ok || len(version) != 16 {
		t.Errorf("cover = %v, want a 16-character version", with["cover"])
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

// A different image hash is a different URL, so a re-enrichment cannot leave a
// browser serving last month's cover (pi-review on #61). It has to re-enrich one
// item: comparing two items passes for any per-item version.
func TestCoverVersionChangesWhenTheImageDoes(t *testing.T) {
	h, library := coverHandler(t, &stubFetcher{body: coverPNG, kind: "image/png"})
	seedCovered(t, library, "a", `{"image":"https://example.test/one.png"}`)

	version := func() string {
		body := decode(t, request(h, http.MethodGet, "/api/items/a", map[string]string{
			identityHeader: "autumn@grove.place",
		}))["item"].(map[string]any)
		value, _ := body["cover"].(string)
		return value
	}

	before := version()
	if before == "" {
		t.Fatal("no cover version before re-enrichment")
	}

	// The same item with a new og:image, which is what a re-enrichment writes.
	if _, err := library.ApplyEnrichment(context.Background(), "a", store.Enrichment{
		OGJSON:     `{"image":"https://example.test/two.png"}`,
		Kind:       model.KindArticle,
		KindSource: model.KindSourceAI,
	}); err != nil {
		t.Fatalf("ApplyEnrichment: %v", err)
	}

	after := version()
	if after == "" {
		t.Fatal("no cover version after re-enrichment")
	}
	if after == before {
		t.Errorf("cover version stayed %q after the image changed: a browser keeps the old cover", after)
	}
}

// `og:image` is legally relative or protocol-relative, and fetch only takes an
// absolute http(s) URL (pi-review on #61).
func TestCoverResolvesARelativeImageAgainstThePage(t *testing.T) {
	fetcher := &stubFetcher{body: coverPNG, kind: "image/png"}
	h, library := coverHandler(t, fetcher)
	if _, err := library.CreateItem(context.Background(), store.ItemInput{
		ID:     "rel",
		URL:    ptr("https://example.test/posts/one"),
		Note:   "",
		OGJSON: `{"image":"/images/og.png"}`,
	}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := library.CreateItem(context.Background(), store.ItemInput{
		ID:     "proto",
		URL:    ptr("https://example.test/posts/two"),
		Note:   "",
		OGJSON: `{"image":"//cdn.example.test/og.png"}`,
	}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	for id, want := range map[string]string{
		"rel":   "https://example.test/images/og.png",
		"proto": "https://cdn.example.test/og.png",
	} {
		fetcher.asked = ""
		rec := request(h, http.MethodGet, "/api/items/"+id+"/cover", map[string]string{
			identityHeader: "autumn@grove.place",
		})
		if rec.Code != http.StatusOK {
			t.Errorf("%s: GET cover = %d, want 200 (resolved against the page)", id, rec.Code)
		}
		if fetcher.asked != want {
			t.Errorf("%s: fetched %q, want %q", id, fetcher.asked, want)
		}
	}

	// A relative image on an item with no page URL cannot be resolved, so it is
	// honestly absent rather than a permanent 404 loop.
	if _, err := library.CreateItem(context.Background(), store.ItemInput{
		ID:     "note",
		Note:   "a note",
		OGJSON: `{"image":"/images/og.png"}`,
	}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	body := decode(t, request(h, http.MethodGet, "/api/items/note", map[string]string{
		identityHeader: "autumn@grove.place",
	}))["item"].(map[string]any)
	if body["cover"] != nil {
		t.Errorf("cover = %v, want absent for an unresolvable relative image", body["cover"])
	}
}

// The URL is versioned, so immutable is now true — but the bytes are
// owner-only, so the cache is private and a validator exists.
func TestCoverHeadersArePrivateVersionedAndValidatable(t *testing.T) {
	h, library := coverHandler(t, &stubFetcher{body: coverPNG, kind: "image/png"})
	seedCovered(t, library, "a", `{"image":"https://example.test/a.png"}`)

	rec := request(h, http.MethodGet, "/api/items/a/cover", map[string]string{
		identityHeader: "autumn@grove.place",
	})
	cache := rec.Header().Get("Cache-Control")
	if strings.Contains(cache, "public") {
		t.Errorf("Cache-Control = %q: this is owner-only content", cache)
	}
	if !strings.Contains(cache, "private") || !strings.Contains(cache, "immutable") {
		t.Errorf("Cache-Control = %q, want private and immutable", cache)
	}
	if got := rec.Header().Get("ETag"); got == "" {
		t.Error("no ETag: a client cannot revalidate this cover")
	}
}

// A cover response is not the shell, so it must not be compressed or wrapped
// in the HTML policy: it is bytes with its own type.
func TestCoverIsServedAsAnImageNotJSON(t *testing.T) {
	// Over the gzip threshold, so the content-type rule is actually exercised
	// rather than the size rule (pi-review on #61).
	big := append(append([]byte{}, coverPNG...), bytes.Repeat([]byte("\x89"), 4096)...)
	h, library := coverHandler(t, &stubFetcher{body: big, kind: "image/png"})
	seedCovered(t, library, "e", `{"image":"https://example.test/a.png"}`)

	rec := request(h, http.MethodGet, "/api/items/e/cover", map[string]string{
		identityHeader:    "autumn@grove.place",
		"Accept-Encoding": "gzip",
	})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q: a cover is served untouched", got)
	}
	if len(rec.Body.Bytes()) != len(big) {
		t.Errorf("body = %d bytes, want the %d fetched", rec.Body.Len(), len(big))
	}
}

var _ cover.Fetcher = (*stubFetcher)(nil)
