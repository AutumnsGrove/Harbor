package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// The fallback in preserveCurrent is not "the database is corrupt", it is "the
// snapshot did not happen". These tests pin that distinction and the journals,
// because a hot journal replayed into a fresh database corrupts a restore.

var preserveNow = func() time.Time { return time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC) }

func TestPreserveCurrentMovesAsideWhenTheSnapshotFails(t *testing.T) {
	// A partially corrupt database still opens; VACUUM INTO is what fails. That
	// is the common case, so the fallback has to key on the snapshot, not Open.
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, store.DBFile)
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00damaged"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, journal := range []string{"-wal", "-shm", "-journal"} {
		if err := os.WriteFile(dbPath+journal, []byte(journal+" bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	backupsDir := filepath.Join(dataDir, "backups")

	preserved, err := preserveCurrent(context.Background(),
		func(context.Context, string) error { return errors.New("database disk image is malformed") },
		backupsDir, dbPath, preserveNow)
	if err != nil {
		t.Fatalf("preserveCurrent: %v", err)
	}
	if !strings.HasSuffix(preserved, ".corrupt") {
		t.Errorf("preserved = %q, want the moved-aside directory", preserved)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the unopenable database is still in place: %v", err)
	}
	// Every companion moved with it: a journal left behind is replayed into the
	// restored database on the next open.
	for _, name := range []string{store.DBFile, store.DBFile + "-wal", store.DBFile + "-shm", store.DBFile + "-journal"} {
		if _, err := os.Stat(filepath.Join(preserved, name)); err != nil {
			t.Errorf("%s was not preserved: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still beside the database", name)
		}
	}
}

func TestPreserveCurrentSnapshotsWhenTheSnapshotWorks(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, store.DBFile)
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("wal"), 0o644); err != nil {
		t.Fatal(err)
	}
	backupsDir := filepath.Join(dataDir, "backups")

	preserved, err := preserveCurrent(context.Background(),
		func(_ context.Context, dest string) error { return os.WriteFile(dest, []byte("snapshot"), 0o644) },
		backupsDir, dbPath, preserveNow)
	if err != nil {
		t.Fatalf("preserveCurrent: %v", err)
	}
	if !strings.HasSuffix(preserved, ".db") {
		t.Errorf("preserved = %q, want a pre-restore snapshot", preserved)
	}
	// The snapshot holds the journal's contents, so the journal must not survive
	// to be replayed into the restored file.
	if _, err := os.Stat(dbPath + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the journal survived a successful snapshot")
	}
}

func TestPreserveCurrentMovesOrphanJournalsAside(t *testing.T) {
	// No database, but a journal: those bytes can still be the only copy of a
	// transaction, so they are kept rather than left to be replayed.
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, store.DBFile)
	if err := os.WriteFile(dbPath+"-wal", []byte("orphan wal"), 0o644); err != nil {
		t.Fatal(err)
	}
	backupsDir := filepath.Join(dataDir, "backups")

	preserved, err := preserveCurrent(context.Background(),
		func(context.Context, string) error { return errors.New("nothing to snapshot") },
		backupsDir, dbPath, preserveNow)
	if err != nil {
		t.Fatalf("preserveCurrent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(preserved, store.DBFile+"-wal")); err != nil {
		t.Errorf("the orphan journal was not preserved: %v", err)
	}
	if _, err := os.Stat(dbPath + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the orphan journal is still beside the database")
	}
}

func TestRestoreOfARenamedBackupStillPrintsTheReplayHint(t *testing.T) {
	// backup.Verify accepts any filename, and the usage text suggests
	// backup.db.zst — so a name the parser cannot read must not panic after the
	// database has already been replaced.
	dataDir := t.TempDir()
	createLibrary(t, dataDir, "a")
	source := readFile(t, takeBackup(t, dataDir))
	renamed := filepath.Join(t.TempDir(), "backup.db.zst")
	if err := os.WriteFile(renamed, source, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"restore", renamed, "--yes"}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor restore: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "next: harbor replay --since") {
		t.Errorf("no replay hint: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "the backup's own date") {
		t.Errorf("a name it cannot read should say so, not panic: %q", stdout.String())
	}
	if got := itemIDs(t, dataDir); len(got) != 1 || got[0] != "a" {
		t.Errorf("items = %v, want the backup's item", got)
	}
}
