// Package store owns SQLite: the file, the pragmas the rest of the program
// depends on, the forward-only migration runner and the snapshots the updater
// takes before it pulls a new image.
//
// Everything above this package talks to typed methods. Nothing outside it sees
// database/sql (docs/plans/README.md, "Package rule"). That is what keeps the
// fuzz targets small and the transactions reviewable.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: CGO_ENABLED=0, so cross-builds stay trivial
)

// DBFile is the one irreplaceable artifact (design.md §3). Snapshots and
// backups exist because nothing here is regenerable.
const DBFile = "harbor.db"

// Store is the live database handle.
type Store struct {
	db   *sql.DB
	path string
}

// Open prepares dataDir/harbor.db: it creates the directory if the volume is
// fresh, proves it can write there, applies the pragmas, and runs the
// migrations. Every failure is fatal and says which step failed — a store that
// half-opens is worse than one that refuses.
func Open(dataDir string) (*Store, error) { return OpenWith(dataDir, slog.Default()) }

// OpenWith is Open with an explicit logger, for the boot integrity check.
func OpenWith(dataDir string, log *slog.Logger) (*Store, error) {
	if err := prepareDir(dataDir); err != nil {
		return nil, err
	}

	path := filepath.Join(dataDir, DBFile)
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// One connection, deliberately. Harbor is a single-user library, so
	// serialising access costs nothing worth measuring and removes an entire
	// class of SQLITE_BUSY failures between an enrichment write and a request.
	// The busy_timeout in the DSN is the second belt, for the snapshot process
	// and the CLI, which are not this pool.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// WAL is persistent per database file, but a store that silently fell back
	// to rollback mode would serialise every reader behind every writer, so it
	// is checked rather than assumed.
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read journal_mode: %w", err)
	}
	if mode != "wal" {
		_ = db.Close()
		return nil, fmt.Errorf("journal_mode is %q, want wal: WAL needs write access to the data directory", mode)
	}
	if err := migrate(ctx, db, migrationsFS()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}

	checkOnBoot(db, log)

	return &Store{db: db, path: path}, nil
}

// Close stops the pool. It is safe to call once; a second call is a no-op.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path is the database file, for logs and the snapshot subcommand.
func (s *Store) Path() string { return s.path }

// dsn builds the connection string. Pragmas live here rather than in an Exec
// after open because the driver applies them to every new connection, including
// one created after a dropped connection.
func dsn(path string) string {
	params := url.Values{}
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "synchronous(NORMAL)")
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "busy_timeout(5000)")
	return "file:" + path + "?" + params.Encode()
}

// prepareDir creates the data directory when the volume is fresh and proves it
// is writable before SQLite gets involved. On the potato the root filesystem is
// read-only, so a missing or wrong volume must be a startup error, not a
// "database is locked" mystery later (plans/00-foundations.md, Risks).
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create data dir %s: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".writable-*")
	if err != nil {
		return fmt.Errorf("data dir %s is not writable: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("data dir %s is not writable: %w", dir, err)
	}
	return nil
}
