package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if body["queueDepth"] != float64(1) {
		t.Errorf("queueDepth = %v, want 1", body["queueDepth"])
	}
	if body["failed"] != float64(0) {
		t.Errorf("failed = %v, want 0", body["failed"])
	}
	// The item was saved just now, so it is inside the week.
	if body["savedThisWeek"] != float64(1) {
		t.Errorf("savedThisWeek = %v, want 1", body["savedThisWeek"])
	}
	// Present and zero, not absent: the tile asks for it from the day it ships.
	if body["mirrorPending"] != float64(0) {
		t.Errorf("mirrorPending = %v, want 0", body["mirrorPending"])
	}
	// Phase 11 owns this one; null is the honest answer until then.
	if value, present := body["lastLinkCheck"]; !present || value != nil {
		t.Errorf("lastLinkCheck = %v (present=%v), want null", value, present)
	}
	if _, present := body["diskUsedPct"]; !present {
		t.Error("diskUsedPct is missing: the data dir exists, so it should be reported")
	}
}

// The newest snapshot's mtime is the answer, and no snapshot is null rather
// than a confident zero (issue #60).
func TestStatusReportsTheNewestBackup(t *testing.T) {
	cfg := testConfig(t)
	backups := filepath.Join(cfg.DataDir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(backups, "harbor-old.db.zst")
	newest := filepath.Join(backups, "harbor-new.db.zst")
	for _, path := range []string{old, newest} {
		if err := os.WriteFile(path, []byte("z"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := time.Date(2026, 11, 1, 3, 10, 0, 0, time.UTC)
	newTime := time.Date(2026, 11, 24, 3, 10, 0, 0, time.UTC)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newest, newTime, newTime); err != nil {
		t.Fatal(err)
	}

	h := New(cfg, slog.New(slog.DiscardHandler), testAssets()).Handler()
	body := decode(t, request(h, http.MethodGet, "/api/status", nil))

	if got := body["lastBackup"]; got != "2026-11-24T03:10:00Z" {
		t.Errorf("lastBackup = %v, want the newest snapshot's UTC mtime", got)
	}
}

func TestStatusWithoutBackupsSaysNull(t *testing.T) {
	cfg := testConfig(t)
	h := New(cfg, slog.New(slog.DiscardHandler), testAssets()).Handler()
	body := decode(t, request(h, http.MethodGet, "/api/status", nil))

	if value, present := body["lastBackup"]; !present || value != nil {
		t.Errorf("lastBackup = %v (present=%v), want null: there is no snapshot", value, present)
	}
}

// A stat that cannot be read omits the field instead of claiming an empty disk.
func TestDiskUsedPctOmittedWhenTheFilesystemCannotBeRead(t *testing.T) {
	if got := diskUsedPct(filepath.Join(t.TempDir(), "does", "not", "exist")); got != nil {
		t.Errorf("diskUsedPct = %v, want nil for a missing directory", *got)
	}
	if got := diskUsedPct(t.TempDir()); got == nil || *got < 0 || *got > 100 {
		t.Errorf("diskUsedPct = %v, want a percentage", got)
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
