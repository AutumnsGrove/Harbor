package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// seed writes a table and n rows through the store's own connection, the same
// way a later phase's write path will.
func seed(t *testing.T, s *Store, n int) {
	t.Helper()
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS notes (body TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := s.db.Exec(`INSERT INTO notes (body) VALUES (?)`, "note"); err != nil {
			t.Fatal(err)
		}
	}
}

func countNotes(t *testing.T, path string) int {
	t.Helper()
	db := openRawDB(t, path)
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM notes`).Scan(&rows); err != nil {
		t.Fatalf("counting notes in %s: %v", path, err)
	}
	return rows
}

func TestSnapshotProducesAVerifiedCopy(t *testing.T) {
	s := openTestStore(t)
	seed(t, s, 3)

	dest := filepath.Join(t.TempDir(), "backups", "pre-update.db")
	if err := s.Snapshot(context.Background(), dest); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	if err := Verify(dest); err != nil {
		t.Errorf("Verify(snapshot) error = %v", err)
	}
	if got := countNotes(t, dest); got != 3 {
		t.Errorf("snapshot holds %d notes, want 3", got)
	}

	// No half-written temp file may survive: the updater looks at the directory
	// and a stray .snapshot-* would be one more thing to explain at 2 a.m.
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".snapshot-") {
			t.Errorf("temp snapshot %s was left behind", entry.Name())
		}
	}
}

func TestSnapshotReplacesThePreviousOne(t *testing.T) {
	s := openTestStore(t)
	dest := filepath.Join(t.TempDir(), "pre-update.db")

	seed(t, s, 1)
	if err := s.Snapshot(context.Background(), dest); err != nil {
		t.Fatalf("first Snapshot() error = %v", err)
	}

	seed(t, s, 2)
	if err := s.Snapshot(context.Background(), dest); err != nil {
		t.Fatalf("second Snapshot() error = %v", err)
	}

	if got := countNotes(t, dest); got != 3 {
		t.Errorf("snapshot holds %d notes, want 3 after the second snapshot", got)
	}
}

func TestSnapshotFailureLeavesTheOldSnapshotIntact(t *testing.T) {
	s := openTestStore(t)
	dir := t.TempDir()
	dest := filepath.Join(dir, "pre-update.db")

	seed(t, s, 2)
	if err := s.Snapshot(context.Background(), dest); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Snapshot(ctx, dest); err == nil {
		t.Fatal("Snapshot() with a cancelled context error = nil, want a rejection")
	}

	// The rollback target is still the last good snapshot, not a truncated file.
	if err := Verify(dest); err != nil {
		t.Errorf("Verify(old snapshot) error = %v after a failed snapshot", err)
	}
	if got := countNotes(t, dest); got != 2 {
		t.Errorf("snapshot holds %d notes after a failed snapshot, want the 2 from the good one", got)
	}
}

func TestSnapshotFailureAfterTheCopyKeepsTheOldSnapshot(t *testing.T) {
	// The cancelled-context case returns before any work, so it cannot prove the
	// temp→rename property. This one fails after VACUUM INTO has written a
	// complete copy and checks that the previous dest is still there, whole.
	s := openTestStore(t)
	dest := filepath.Join(t.TempDir(), "pre-update.db")

	seed(t, s, 2)
	if err := s.Snapshot(context.Background(), dest); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}

	original := verifySnapshot
	t.Cleanup(func() { verifySnapshot = original })
	verifySnapshot = func(string) error { return errors.New("integrity check failed") }

	seed(t, s, 1)
	if err := s.Snapshot(context.Background(), dest); err == nil {
		t.Fatal("Snapshot() with a failing verification error = nil, want a rejection")
	}
	verifySnapshot = original

	if err := Verify(dest); err != nil {
		t.Errorf("the previous snapshot no longer verifies: %v", err)
	}
	if got := countNotes(t, dest); got != 2 {
		t.Errorf("dest holds %d notes, want the 2 from the last good snapshot", got)
	}

	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".snapshot-") {
			t.Errorf("temp snapshot %s was left behind", entry.Name())
		}
	}
}

func TestSnapshotDuringConcurrentWrites(t *testing.T) {
	s := openTestStore(t)
	seed(t, s, 1)

	// A second connection stands in for the enrichment loop or the CLI writing
	// while the updater snapshots.
	writer := openRawDB(t, s.Path())
	if _, err := writer.Exec(`CREATE TABLE IF NOT EXISTS notes (body TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := writer.Exec(`INSERT INTO notes (body) VALUES ('concurrent')`); err != nil {
					t.Errorf("concurrent write: %v", err)
					return
				}
			}
		}
	}()

	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		dest := filepath.Join(dir, "pre-update.db")
		if err := s.Snapshot(context.Background(), dest); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Snapshot() %d during writes error = %v", i+1, err)
		}
		if err := Verify(dest); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("snapshot %d is not intact: %v", i+1, err)
		}
		time.Sleep(time.Millisecond)
	}

	close(stop)
	wg.Wait()
}

func TestVerifyRejectsACorruptedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(path, []byte("this is not a database, it is a poem"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path); err == nil {
		t.Fatal("Verify() on a non-database error = nil, want a rejection")
	}
}
