package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
