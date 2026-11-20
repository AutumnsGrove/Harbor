package store

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func TestPragmasAreAppliedToEveryConnection(t *testing.T) {
	s := openTestStore(t)
	want := map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"foreign_keys": "1",
		"busy_timeout": "5000",
	}
	for pragma, expected := range want {
		var got string
		if err := s.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if got != expected {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, expected)
		}
	}
}

// plans/02 task: integrity check on start (log, don't fail).
func TestOpenLogsAHealthyIntegrityCheck(t *testing.T) {
	logger, buf := captureLog()
	s, err := OpenWith(t.TempDir(), logger)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	out := buf.String()
	if !strings.Contains(out, "integrity check ok") {
		t.Errorf("log lacks a healthy integrity line:\n%s", out)
	}
	if strings.Contains(out, "level=ERROR") || strings.Contains(out, "level=WARN") {
		t.Errorf("a healthy database logged a problem:\n%s", out)
	}
}

func TestOpenSurvivesAndReportsACorruptPage(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	filler := strings.Repeat("filler text for the index ", 20)
	for i := 0; i < 400; i++ {
		exec(t, first, `INSERT INTO items (id, title, note) VALUES (?, ?, ?)`, "id-"+strings.Repeat("x", i%7)+string(rune('A'+i%26))+strings.Repeat("y", i), filler, filler)
	}
	_ = first.Close()

	// Overwrite a page well past the schema page: the file still opens, but the
	// b-tree behind it is broken.
	path := filepath.Join(dir, DBFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 4096*12 {
		t.Fatalf("test database is only %d bytes", len(data))
	}
	for i := 4096 * 6; i < 4096*8; i++ {
		data[i] = 0xFF
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")

	logger, buf := captureLog()
	second, err := OpenWith(dir, logger)
	if err != nil {
		t.Fatalf("Open() on a corrupt database error = %v, want it to log and carry on", err)
	}
	defer second.Close()
	if out := buf.String(); !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "integrity") {
		t.Errorf("a corrupt database was not reported:\n%s", out)
	}
}

func TestIntegrityCheckReturnsTheProblemsItFound(t *testing.T) {
	s := openTestStore(t)
	problems, err := integrityCheck(context.Background(), s.db)
	if err != nil || len(problems) != 0 {
		t.Errorf("integrityCheck() = %v, %v on a healthy database, want none", problems, err)
	}
}
