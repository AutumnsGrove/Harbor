package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

func updateServer(t *testing.T) (*Server, build.Config) {
	t.Helper()
	cfg := testConfig(t)
	return New(cfg, nil, testAssets()), cfg
}

func TestUpdateCheckWritesOneSignalFile(t *testing.T) {
	s, cfg := updateServer(t)
	h := s.Handler()

	rec := authed(h, "POST", "/api/update/check")
	if rec.Code != 202 {
		t.Fatalf("POST /api/update/check = %d, want 202", rec.Code)
	}

	entries, err := os.ReadDir(cfg.UpdateDir)
	if err != nil {
		t.Fatalf("reading the update dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != updateCheckFile {
		t.Fatalf("update dir holds %v, want exactly [%s]", names(entries), updateCheckFile)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatalf("stat %s: %v", updateCheckFile, err)
	}
	if info.Size() != 0 {
		t.Errorf("%s is %d bytes, want the check signal to be empty", updateCheckFile, info.Size())
	}
}

func TestUpdateStartWritesTheLiteralLatest(t *testing.T) {
	s, cfg := updateServer(t)

	if rec := authed(s.Handler(), "POST", "/api/update/start"); rec.Code != 202 {
		t.Fatalf("POST /api/update/start = %d, want 202", rec.Code)
	}

	contents, err := os.ReadFile(filepath.Join(cfg.UpdateDir, updateRequestFile))
	if err != nil {
		t.Fatalf("reading %s: %v", updateRequestFile, err)
	}
	if got, want := string(contents), latestImage+"\n"; got != want {
		t.Fatalf("%s = %q, want %q", updateRequestFile, got, want)
	}
	// design.md §3: the app never names an image. If this ever regresses, the app
	// could point the watcher at an artifact of its choosing.
	for _, forbidden := range []string{"ghcr.io", "sha256", ":"} {
		if strings.Contains(string(contents), forbidden) {
			t.Errorf("%s contains %q; the app must write only %q", updateRequestFile, forbidden, latestImage)
		}
	}
}

func TestUpdatePathsCannotBeInfluencedByTheRequest(t *testing.T) {
	s, cfg := updateServer(t)
	h := s.Handler()

	// Query strings, fragments and traversal-looking paths must all land on the
	// same constant file, or nowhere at all.
	for _, target := range []string{
		"/api/update/check?file=../../etc/passwd",
		"/api/update/check?path=/tmp/evil",
		"/api/update/check/../../evil",
	} {
		rec := authed(h, "POST", target)
		// An unclean path is redirected by ServeMux, which is also the right
		// outcome: the redirect target is the constant route, never a path the
		// caller supplied.
		if rec.Code != 202 && rec.Code != 404 && rec.Code != 307 {
			t.Errorf("POST %s = %d, want 202, 307 or 404", target, rec.Code)
		}
	}

	entries, err := os.ReadDir(cfg.UpdateDir)
	if err != nil {
		t.Fatalf("reading the update dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != updateCheckFile {
		t.Fatalf("update dir holds %v after traversal attempts, want exactly [%s]", names(entries), updateCheckFile)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cfg.UpdateDir), "evil")); err == nil {
		t.Fatal("a traversal attempt created a file outside the update dir")
	}
}

func TestUpdateAvailable(t *testing.T) {
	t.Run("before the watcher has checked", func(t *testing.T) {
		s, _ := updateServer(t)
		rec := authed(s.Handler(), "GET", "/api/update/available")

		if rec.Code != 200 {
			t.Fatalf("GET /api/update/available = %d, want 200", rec.Code)
		}
		if body := decode(t, rec); body["available"] != false {
			t.Errorf("body = %v, want available false", body)
		}
	})

	t.Run("passes the watcher's JSON through untouched", func(t *testing.T) {
		s, cfg := updateServer(t)
		watcher := `{"available":true,"digest":"sha256:abc","version":"r12.abcdef0"}`
		if err := os.WriteFile(filepath.Join(cfg.UpdateDir, updateAvailableFile), []byte(watcher), 0o644); err != nil {
			t.Fatal(err)
		}

		rec := authed(s.Handler(), "GET", "/api/update/available")
		if got := strings.TrimSpace(rec.Body.String()); got != watcher {
			t.Errorf("body = %s, want the watcher's JSON byte-for-byte: %s", got, watcher)
		}
	})

	t.Run("refuses invalid JSON instead of guessing", func(t *testing.T) {
		s, cfg := updateServer(t)
		if err := os.WriteFile(filepath.Join(cfg.UpdateDir, updateAvailableFile), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}

		rec := authed(s.Handler(), "GET", "/api/update/available")
		if rec.Code != 500 {
			t.Fatalf("GET /api/update/available with broken JSON = %d, want 500", rec.Code)
		}
	})
}

func TestUpdateStatus(t *testing.T) {
	t.Run("unknown before the first run", func(t *testing.T) {
		s, _ := updateServer(t)
		rec := authed(s.Handler(), "GET", "/api/update/status")
		if body := decode(t, rec); body["status"] != "unknown" {
			t.Errorf("body = %v, want status unknown", body)
		}
	})

	t.Run("passes the watcher's status through", func(t *testing.T) {
		s, cfg := updateServer(t)
		watcher := `{"status":"ok","version":"r12.abcdef0","at":"2026-11-18T00:00:00Z"}`
		if err := os.WriteFile(filepath.Join(cfg.UpdateDir, updateStatusFile), []byte(watcher), 0o644); err != nil {
			t.Fatal(err)
		}
		rec := authed(s.Handler(), "GET", "/api/update/status")
		if got := strings.TrimSpace(rec.Body.String()); got != watcher {
			t.Errorf("body = %s, want %s", got, watcher)
		}
	})
}

func TestUpdateSignalFailureIsReported(t *testing.T) {
	cfg := testConfig(t)
	// Point the update dir at a file: MkdirAll cannot create a directory where
	// a regular file exists.
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.UpdateDir = file
	h := New(cfg, nil, testAssets()).Handler()

	rec := authed(h, "POST", "/api/update/check")
	if rec.Code != 500 {
		t.Fatalf("POST /api/update/check with an unusable dir = %d, want 500", rec.Code)
	}
	if body := decode(t, rec); body["error"] == nil {
		t.Errorf("body = %v, want a JSON error", body)
	}
}

func TestUpdateDirIsCreatedOnFirstUse(t *testing.T) {
	cfg := testConfig(t)
	cfg.UpdateDir = filepath.Join(t.TempDir(), "not-yet", "update-signal")
	h := New(cfg, nil, testAssets()).Handler()

	if rec := authed(h, "POST", "/api/update/check"); rec.Code != 202 {
		t.Fatalf("POST /api/update/check with a missing dir = %d, want 202", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(cfg.UpdateDir, updateCheckFile)); err != nil {
		t.Fatalf("%s was not written: %v", updateCheckFile, err)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}
