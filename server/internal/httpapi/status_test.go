package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// plans/02 test 13's sibling: /api/status is the header-less exemption, so it
// reports counts from the library and still exposes nothing else.
func TestStatusReportsLibraryCounts(t *testing.T) {
	cfg := testConfig(t)
	library, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = library.Close() })

	// A fresh item is queued, so the live count and the pending count rise
	// together; the store test covers the failed bucket.
	if _, err := library.CreateItem(context.Background(), store.ItemInput{ID: "a", Note: "x"}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	h := New(cfg, slog.New(slog.DiscardHandler), testAssets(), WithStore(library)).Handler()
	rec := request(h, http.MethodGet, "/api/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/status = %d, want 200", rec.Code)
	}
	body := decode(t, rec)
	if body["items"] != float64(1) {
		t.Errorf("items = %v, want 1", body["items"])
	}
	if body["pending"] != float64(1) {
		t.Errorf("pending = %v, want 1", body["pending"])
	}
	if body["failed"] != float64(0) {
		t.Errorf("failed = %v, want 0", body["failed"])
	}
}

// A database that cannot be read must not make /api/status fail: the status
// page is how "is it working?" gets answered, so it has to answer about a
// broken database too (design.md §3).
func TestStatusAnswersWithAClosedStore(t *testing.T) {
	cfg := testConfig(t)
	library, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := library.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	h := New(cfg, slog.New(slog.DiscardHandler), testAssets(), WithStore(library)).Handler()
	rec := request(h, http.MethodGet, "/api/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/status with a closed store = %d, want 200", rec.Code)
	}
	if body := decode(t, rec); body["items"] != float64(0) {
		t.Errorf("items = %v, want 0 when the count cannot be read", body["items"])
	}
}
