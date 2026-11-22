package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

// The backup command is run by a systemd timer and by a tired person at 2 a.m.
// Both deserve the same answer, so the tests drive the command itself.

func TestBackupCommandWritesAVerifiableBackup(t *testing.T) {
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "backups")

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"backup"}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor backup: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "backed up") || !strings.Contains(stdout.String(), "1 backup(s) kept") {
		t.Errorf("stdout = %q", stdout.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("backups dir: %v\n%s", err, stdout.String())
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".db.zst") {
		t.Fatalf("backups = %v", entries)
	}

	// --list shows it, and --verify opens it.
	stdout.Reset()
	if err := run(context.Background(), []string{"backup", "--list"}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor backup --list: %v", err)
	}
	if !strings.Contains(stdout.String(), entries[0].Name()) {
		t.Errorf("--list does not name the backup: %q", stdout.String())
	}

	stdout.Reset()
	if err := run(context.Background(), []string{"backup", "--verify"}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor backup --verify: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "ok") {
		t.Errorf("--verify did not report ok: %q", stdout.String())
	}
}

func TestBackupListAndVerifyAreSeparateRuns(t *testing.T) {
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"backup", "--list", "--verify"}, exportEnv(t, t.TempDir()), &stdout)
	if err == nil || !strings.Contains(err.Error(), "separate runs") {
		t.Fatalf("backup --list --verify = %v", err)
	}
}

func TestBackupCommandOnAEmptyDirectorySaysSo(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"backup", "--list"}, exportEnv(t, t.TempDir()), &stdout); err != nil {
		t.Fatalf("backup --list: %v", err)
	}
	if !strings.Contains(stdout.String(), "no backups") {
		t.Errorf("stdout = %q", stdout.String())
	}
}

func TestBackupVerifyFailsOnACorruptBackup(t *testing.T) {
	dataDir := t.TempDir()
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"backup"}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor backup: %v", err)
	}
	dir := filepath.Join(dataDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("backups = %v, %v", entries, err)
	}
	if err := os.Truncate(filepath.Join(dir, entries[0].Name()), 20); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	err = run(context.Background(), []string{"backup", "--verify"}, exportEnv(t, dataDir), &stdout)
	if err == nil {
		t.Fatalf("--verify accepted a corrupt backup:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "FAILED") || !strings.Contains(err.Error(), "did not verify") {
		t.Errorf("stdout = %q, err = %v", stdout.String(), err)
	}
}

func TestBackupFailsLoudWhenAnOldBackupCannotBeVerified(t *testing.T) {
	// Retention keeps a broken old backup — it may be the only copy that opens —
	// but a nightly job that says nothing about it is a job nobody can trust, so
	// the run names it and exits non-zero.
	dataDir := t.TempDir()
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 31; day++ {
		name := fmt.Sprintf("harbor-202610%02dT120000Z.db.zst", day)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("not a backup"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var stdout bytes.Buffer
	err := run(context.Background(), []string{"backup"}, exportEnv(t, dataDir), &stdout)
	if err == nil {
		t.Fatalf("backup over 31 unverifiable files exited zero:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "did not verify and was kept") {
		t.Errorf("stdout does not name the kept-broken backups:\n%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "failed verification") {
		t.Errorf("err = %v, want a loud failure", err)
	}
	// And none of them were deleted.
	left, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(left) != 32 {
		t.Errorf("backups dir holds %d files, want the new one plus the 31 kept", len(left))
	}
}

func TestBackupUploadsThroughTheWorker(t *testing.T) {
	// The potato holds no R2 credential, so the Worker is the only way the backup
	// leaves the box. The test asserts the PUT it makes, path and headers.
	var (
		mu      sync.Mutex
		methods []string
		paths   []string
		sha     string
		length  int64
		body    []byte
	)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/backups" {
			// A remote copy the local disk has never seen. The upload must NOT
			// delete it: the off-box copies exist because the potato can be lost,
			// so their fate cannot depend on what is on this disk.
			_, _ = w.Write([]byte(`{"backups":[{"name":"harbor-20261101T000000Z.db.zst","bytes":5,"uploaded":1}]}`))
			return
		}
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		if r.Method == http.MethodPut {
			// Only the upload carries the bytes; the DELETE after it has none.
			sha = r.Header.Get("X-Harbor-Sha256")
			length = r.ContentLength
			body = data
		}
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("{}"))
	}))
	defer worker.Close()

	dataDir := t.TempDir()
	env := func(name string) string {
		switch name {
		case build.EnvDataDir:
			return dataDir
		case build.EnvDeployment:
			return "dev"
		case build.EnvDevNoAuth:
			return "1"
		case build.EnvWorkerURL:
			return worker.URL
		case build.EnvWorkerToken:
			return "potato-token"
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"backup", "--upload"}, env, &stdout); err != nil {
		t.Fatalf("harbor backup --upload: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "uploaded") {
		t.Errorf("stdout = %q", stdout.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 1 || methods[0] != http.MethodPut {
		t.Fatalf("requests = %v, want the upload and no deletion", methods)
	}
	if !strings.HasPrefix(paths[0], "/backups/harbor-") || !strings.HasSuffix(paths[0], ".db.zst") {
		t.Errorf("path = %q", paths[0])
	}
	if length <= 0 || int64(len(body)) != length {
		t.Errorf("uploaded %d bytes with content-length %d", len(body), length)
	}
	sum := sha256.Sum256(body)
	if sha != hex.EncodeToString(sum[:]) {
		t.Errorf("uploaded sha = %q, want the digest of the bytes", sha)
	}
}

func TestBackupPrunesR2ToItsOwnWindow(t *testing.T) {
	// R2 keeps thirty regardless of --keep and regardless of how many backups the
	// local disk holds, because design §11 says three on the SD card and thirty
	// off the box.
	var (
		mu      sync.Mutex
		deleted []string
	)
	var listing strings.Builder
	listing.WriteString(`{"backups":[`)
	start := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	for day := 0; day < 32; day++ {
		if day > 0 {
			listing.WriteString(",")
		}
		name := "harbor-" + start.AddDate(0, 0, day).Format("20060102T150405Z") + ".db.zst"
		fmt.Fprintf(&listing, `{"name":%q,"bytes":5,"uploaded":1}`, name)
	}
	listing.WriteString(`]}`)

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/backups":
			_, _ = w.Write([]byte(listing.String()))
		case r.Method == http.MethodDelete:
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"deleted":true}`))
		default:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer worker.Close()

	dataDir := t.TempDir()
	env := func(name string) string {
		switch name {
		case build.EnvDataDir:
			return dataDir
		case build.EnvDeployment:
			return "dev"
		case build.EnvDevNoAuth:
			return "1"
		case build.EnvWorkerURL:
			return worker.URL
		case build.EnvWorkerToken:
			return "potato-token"
		default:
			return ""
		}
	}

	var stdout bytes.Buffer
	// --keep 3 must not shrink R2: it is the SD card's window, not the off-box one.
	if err := run(context.Background(), []string{"backup", "--upload", "--keep", "3"}, env, &stdout); err != nil {
		t.Fatalf("harbor backup --upload: %v\n%s", err, stdout.String())
	}

	mu.Lock()
	defer mu.Unlock()
	// The two oldest of the 32 go; the newest 30 stay, which is the whole window.
	if len(deleted) != 2 {
		t.Fatalf("deleted %v, want the two oldest", deleted)
	}
	for _, path := range deleted {
		if path != "/backups/harbor-20261001T030000Z.db.zst" && path != "/backups/harbor-20261002T030000Z.db.zst" {
			t.Errorf("deleted %q, want only the two oldest", path)
		}
	}
	if !strings.Contains(stdout.String(), "keeping 30") {
		t.Errorf("stdout does not say what it kept: %q", stdout.String())
	}
}

func TestBackupUploadNeedsTheWorker(t *testing.T) {
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"backup", "--upload"}, exportEnv(t, t.TempDir()), &stdout)
	if err == nil || !strings.Contains(err.Error(), build.EnvWorkerURL) {
		t.Fatalf("backup --upload with no Worker = %v", err)
	}
}

func TestHumanSize(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{512, "512B"},
		{2048, "2.0KiB"},
		{5 * 1024 * 1024, "5.0MiB"},
	} {
		if got := humanSize(tc.in); got != tc.want {
			t.Errorf("humanSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
