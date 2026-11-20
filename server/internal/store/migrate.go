package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed all:migrations
var embeddedMigrations embed.FS

// migrationsFS is the shipped migration set: 0001 creates the whole schema from
// design.md §7, and later phases only add numbered files.
func migrationsFS() fs.FS { return embeddedMigrations }

// namePattern is the only shape a migration may have. A stray file in the
// directory is a mistake worth failing on: silently skipping it would mean
// shipping a schema that is not the one reviewed.
var namePattern = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// historyDDL is design.md §7's schema_migrations. Migration 0001 creates it as
// part of the schema; the runner repeats the statement (IF NOT EXISTS) so that a
// test migration set without a 0001 still gets its history.
const historyDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	applied_at DATETIME NOT NULL
)`

// migrate applies every unapplied migration in order, each in its own
// transaction. PRAGMA user_version is the fast check (one integer, no table
// scan) and schema_migrations is the history; both change inside the same
// transaction as the migration, so they cannot disagree. Migrations are
// forward-only (design.md §3): there is no down step, because a down step that
// deletes data is not something a tired operator should be able to reach for.
func migrate(ctx context.Context, db *sql.DB, files fs.FS) error {
	migrations, err := readMigrations(files)
	if err != nil {
		return err
	}

	var current int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	if current > len(migrations) {
		return fmt.Errorf("database is at schema version %d but this build knows only %d: it was written by a newer Harbor, and migrations only go forward", current, len(migrations))
	}

	if err := dropEmptyLegacyHistory(ctx, db); err != nil {
		return err
	}

	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}
		if err := applyOne(ctx, db, migration); err != nil {
			return err
		}
	}
	return nil
}

// dropEmptyLegacyHistory retires the schema_migrations table phase 0 created.
// Phase 0 shipped no migrations, so on every deployed database that table is
// empty and its TEXT version column is the only thing wrong with it; 0001
// creates the table design §7 specifies. A legacy table that holds rows would
// mean history this code does not understand, so that refuses to boot rather
// than guess.
func dropEmptyLegacyHistory(ctx context.Context, db *sql.DB) error {
	var kind string
	err := db.QueryRowContext(ctx,
		`SELECT type FROM pragma_table_info('schema_migrations') WHERE name = 'version'`).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !strings.EqualFold(kind, "TEXT")) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect schema_migrations: %w", err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&rows); err != nil {
		return fmt.Errorf("inspect schema_migrations: %w", err)
	}
	if rows > 0 {
		return fmt.Errorf("schema_migrations is the phase-0 shape but holds %d rows: refusing to guess at that history", rows)
	}
	if _, err := db.ExecContext(ctx, `DROP TABLE schema_migrations`); err != nil {
		return fmt.Errorf("replace the phase-0 schema_migrations: %w", err)
	}
	return nil
}

type migration struct {
	version int
	name    string
	body    string
}

// readMigrations loads the set and checks it is strictly sequential from 1:
// design.md §7 numbers migrations in merge order, so a gap means one went
// missing.
func readMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	var migrations []migration
	seen := make(map[int]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := namePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migration %q is not named NNNN_snake_case.sql", entry.Name())
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("migration %q: %w", entry.Name(), err)
		}
		if seen[version] {
			return nil, fmt.Errorf("version %04d is claimed by more than one migration", version)
		}
		seen[version] = true

		body, err := fs.ReadFile(files, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{version: version, name: match[2], body: string(body)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	for i, m := range migrations {
		if want := i + 1; m.version != want {
			return nil, fmt.Errorf("migrations are not sequential: expected %04d, found %04d", want, m.version)
		}
	}
	return migrations, nil
}

func applyOne(ctx context.Context, db *sql.DB, migration migration) error {
	label := fmt.Sprintf("%04d_%s", migration.version, migration.name)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", label, err)
	}
	defer func() { _ = tx.Rollback() }()

	if strings.TrimSpace(migration.body) != "" {
		if _, err := tx.ExecContext(ctx, migration.body); err != nil {
			return fmt.Errorf("apply migration %s: %w", label, err)
		}
	}
	if _, err := tx.ExecContext(ctx, historyDDL); err != nil {
		return fmt.Errorf("record migration %s: %w", label, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		migration.version, migration.name,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", label, err)
	}
	// PRAGMA takes no bound parameters; the value is an int we parsed ourselves.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, migration.version)); err != nil {
		return fmt.Errorf("set user_version for %s: %w", label, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", label, err)
	}
	return nil
}
