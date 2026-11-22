package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// openRawDB is a second, independent connection to the same file. Tests use it
// to stand in for another process (the CLI, the snapshot subcommand), which is
// the situation the real snapshot has to survive.
func openRawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("opening a second connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`SELECT 1`); err != nil {
		t.Fatalf("second connection is not usable: %v", err)
	}
	return db
}

func TestOpenCreatesAUsableStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(filepath.Join(dir, DBFile)); err != nil {
		t.Errorf("%s does not exist after Open(): %v", DBFile, err)
	}
	if got, want := s.Path(), filepath.Join(dir, DBFile); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}

	// WAL is what lets the snapshot run while requests are in flight; a silent
	// fallback to rollback journal is a bug worth failing on (design.md §3).
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}

	var foreignKeys int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	var applied int
	if err := s.db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&applied); err != nil {
		t.Fatalf("schema_migrations: %v", err)
	}
	shipped, err := readMigrations(migrationsFS())
	if err != nil {
		t.Fatalf("readMigrations: %v", err)
	}
	if applied != len(shipped) {
		t.Errorf("schema_migrations holds %d rows, want %d (every shipped migration)", applied, len(shipped))
	}
}

func TestOpenRefusesAnUnwritableDataDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions are not enforced")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	if _, err := Open(filepath.Join(parent, "data")); err == nil {
		t.Fatal("Open() on a read-only parent error = nil, want a rejection")
	}
}

func TestOpenReportsABadDatabaseFile(t *testing.T) {
	dir := t.TempDir()
	// A directory where the database file should be: writable, but not a file.
	if err := os.Mkdir(filepath.Join(dir, DBFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("Open() with a directory as the database error = nil, want a rejection")
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}
