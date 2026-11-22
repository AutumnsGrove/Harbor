package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/datalock"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Restore is the only destructive command in the binary, so every one of its
// refusals is a test: no lock, no --yes, a backup that will not verify, and a
// current database it cannot preserve.
//
// The tests drive `harbor` itself, because the thing that must be true is about
// the files on disk afterwards.

func createLibrary(t *testing.T, dataDir string, itemID string) {
	t.Helper()
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()
	if _, err := library.CreateItem(context.Background(), store.ItemInput{ID: itemID, Note: "note " + itemID}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
}

func itemIDs(t *testing.T, dataDir string) []string {
	t.Helper()
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()
	items, err := library.ListItems(context.Background(), store.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}

// takeBackup writes one backup into dataDir and returns its path.
func takeBackup(t *testing.T, dataDir string) string {
	t.Helper()
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"backup"}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor backup: %v\n%s", err, stdout.String())
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
	if err != nil {
		t.Fatalf("backups dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".db.zst") {
			return filepath.Join(dataDir, "backups", entry.Name())
		}
	}
	t.Fatal("no backup was written")
	return ""
}

func TestRestoreRefusesWhileTheDatabaseIsInUse(t *testing.T) {
	dataDir := t.TempDir()
	createLibrary(t, dataDir, "a")
	backupPath := takeBackup(t, dataDir)

	held, err := datalock.Acquire(dataDir)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer held.Release()

	var stdout bytes.Buffer
	err = run(context.Background(), []string{"restore", backupPath, "--yes"}, exportEnv(t, dataDir), &stdout)
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("restore with the lock held = %v, want a refusal naming the lock", err)
	}
	if !strings.Contains(err.Error(), "stop the container") {
		t.Errorf("err = %v, want it to say what to do", err)
	}
}

func TestRestoreRefusesWithoutYesAndChangesNothing(t *testing.T) {
	dataDir := t.TempDir()
	createLibrary(t, dataDir, "a")
	backupPath := takeBackup(t, dataDir)
	before := readFile(t, filepath.Join(dataDir, store.DBFile))

	var stdout bytes.Buffer
	err := run(context.Background(), []string{"restore", backupPath}, exportEnv(t, dataDir), &stdout)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("restore without --yes = %v", err)
	}
	if after := readFile(t, filepath.Join(dataDir, store.DBFile)); !bytes.Equal(before, after) {
		t.Error("a refused restore changed the database")
	}
}

func TestRestoreDryRunChangesNothing(t *testing.T) {
	dataDir := t.TempDir()
	createLibrary(t, dataDir, "a")
	backupPath := takeBackup(t, dataDir)
	before := readFile(t, filepath.Join(dataDir, store.DBFile))

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"restore", backupPath, "--dry-run"}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("restore --dry-run: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "dry run: nothing changed") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if after := readFile(t, filepath.Join(dataDir, store.DBFile)); !bytes.Equal(before, after) {
		t.Error("a dry run changed the database")
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "pre-restore-") {
			t.Errorf("a dry run preserved a snapshot: %s", entry.Name())
		}
	}
}

func TestRestoreReplacesTheDatabaseAndKeepsTheOldOne(t *testing.T) {
	dataDir := t.TempDir()
	createLibrary(t, dataDir, "old")
	backupPath := takeBackup(t, dataDir)

	// A save that happened after the backup: the restore must lose it, and the
	// pre-restore snapshot must keep it.
	createLibrary(t, dataDir, "newer")

	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"restore", backupPath, "--yes"}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor restore: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "next: harbor replay --since") {
		t.Errorf("the plan does not say what to run next: %q", stdout.String())
	}

	got := itemIDs(t, dataDir)
	if len(got) != 1 || got[0] != "old" {
		t.Fatalf("items after restore = %v, want just the backup's", got)
	}

	// The current database is recoverable: a pre-restore snapshot holds the save
	// the restore dropped.
	var preserved string
	entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "pre-restore-") {
			preserved = filepath.Join(dataDir, "backups", entry.Name())
		}
	}
	if preserved == "" {
		t.Fatalf("no pre-restore snapshot in %v", entries)
	}
	if err := store.Verify(preserved); err != nil {
		t.Errorf("the pre-restore snapshot does not verify: %v", err)
	}
}

func TestRestoreOfACorruptBackupLeavesTheDatabaseAlone(t *testing.T) {
	dataDir := t.TempDir()
	createLibrary(t, dataDir, "a")
	backupPath := takeBackup(t, dataDir)
	if err := os.Truncate(backupPath, 20); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, filepath.Join(dataDir, store.DBFile))

	var stdout bytes.Buffer
	err := run(context.Background(), []string{"restore", backupPath, "--yes"}, exportEnv(t, dataDir), &stdout)
	if err == nil {
		t.Fatal("restoring a corrupt backup should fail")
	}
	if !strings.Contains(err.Error(), "restore:") {
		t.Errorf("err = %v", err)
	}
	if after := readFile(t, filepath.Join(dataDir, store.DBFile)); !bytes.Equal(before, after) {
		t.Error("a failed restore changed the database")
	}
	if got := itemIDs(t, dataDir); len(got) != 1 || got[0] != "a" {
		t.Errorf("items = %v, want the library untouched", got)
	}
}

func TestRestoreNeedsABackupPath(t *testing.T) {
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"restore"}, exportEnv(t, t.TempDir()), &stdout)
	if err == nil || !strings.Contains(err.Error(), "one backup file") {
		t.Fatalf("restore with no path = %v", err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return body
}
