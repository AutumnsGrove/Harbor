package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
	if body["items_total"] != float64(1) {
		t.Errorf("items = %v, want 1", body["items_total"])
	}
	if body["queue_depth"] != float64(1) {
		t.Errorf("queueDepth = %v, want 1", body["queue_depth"])
	}
	if body["failed"] != float64(0) {
		t.Errorf("failed = %v, want 0", body["failed"])
	}
	// The item was saved just now, so it is inside the week.
	if body["saved_this_week"] != float64(1) {
		t.Errorf("savedThisWeek = %v, want 1", body["saved_this_week"])
	}
	// Present and zero, not absent: the tile asks for it from the day it ships.
	if body["mirror_pending"] != float64(0) {
		t.Errorf("mirrorPending = %v, want 0", body["mirror_pending"])
	}
	// Phase 11 owns this one; null is the honest answer until then.
	if value, present := body["last_link_check"]; !present || value != nil {
		t.Errorf("lastLinkCheck = %v (present=%v), want null", value, present)
	}
	if _, present := body["disk_used_pct"]; !present {
		t.Error("diskUsedPct is missing: the data dir exists, so it should be reported")
	}
}

// The newest snapshot's date is the answer — from its name, never from an mtime,
// and only for a finished `harbor-<date>.db.zst` (pi-review on #62).
func TestStatusReportsTheNewestBackup(t *testing.T) {
	cfg := testConfig(t)
	backups := filepath.Join(cfg.DataDir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatal(err)
	}
	// Only the finished snapshots count; the compression's own temp file and an
	// unrelated file are deliberately present.
	for _, name := range []string{
		"harbor-20261101T031000Z.db.zst",
		"harbor-20261124T031000Z.db.zst",
		".backup-9f2c1a.zst",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(backups, name), []byte("z"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	h := New(cfg, slog.New(slog.DiscardHandler), testAssets()).Handler()
	body := decode(t, request(h, http.MethodGet, "/api/status", nil))

	if got := body["last_backup"]; got != "2026-11-24T03:10:00Z" {
		t.Errorf("last_backup = %v, want the date in the newest snapshot's name", got)
	}
}

// A backup being written right now is a .zst in the same directory with a
// current mtime; it is not a backup until it is renamed (pi-review on #62).
func TestStatusIgnoresATempFileThatLooksLikeABackup(t *testing.T) {
	cfg := testConfig(t)
	backups := filepath.Join(cfg.DataDir, "backups")
	if err := os.MkdirAll(backups, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backups, ".backup-123456789.zst"), []byte("z"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := New(cfg, slog.New(slog.DiscardHandler), testAssets()).Handler()
	body := decode(t, request(h, http.MethodGet, "/api/status", nil))

	if value, present := body["last_backup"]; !present || value != nil {
		t.Errorf("last_backup = %v (present=%v), want null: a temp file is not a snapshot", value, present)
	}
}

func TestStatusWithoutBackupsSaysNull(t *testing.T) {
	cfg := testConfig(t)
	h := New(cfg, slog.New(slog.DiscardHandler), testAssets()).Handler()
	body := decode(t, request(h, http.MethodGet, "/api/status", nil))

	if value, present := body["last_backup"]; !present || value != nil {
		t.Errorf("last_backup = %v (present=%v), want null: there is no snapshot", value, present)
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
	if body := decode(t, rec); body["items_total"] != float64(0) {
		t.Errorf("items = %v, want 0 when the count cannot be read", body["items_total"])
	}
}
