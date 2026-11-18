package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

//go:embed all:migrations
var embeddedMigrations embed.FS

// migrationsFS is the shipped migration set. It is empty in phase 0 and 0001
// creates the whole schema from design.md §7 in phase 2.
func migrationsFS() fs.FS { return embeddedMigrations }

// namePattern is the only shape a migration may have. A stray file in the
// directory is a mistake worth failing on: silently skipping it would mean
// shipping a schema that is not the one reviewed.
var namePattern = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// migrate applies every unapplied migration in order, each in its own
// transaction, and records it in schema_migrations. Migrations are forward-only
// (design.md §3): there is no down step, because a down step that deletes data
// is not something a tired operator should be able to reach for.
func migrate(ctx context.Context, db *sql.DB, files fs.FS) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	migrations, err := readMigrations(files)
	if err != nil {
		return err
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	for _, migration := range migrations {
		if applied[migration.version] {
			continue
		}
		if err := applyOne(ctx, db, migration); err != nil {
			return err
		}
	}
	return nil
}

type migration struct {
	version string
	body    string
}

func readMigrations(files fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	var migrations []migration
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := namePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("migration %q is not named NNNN_snake_case.sql", entry.Name())
		}
		version := match[1]
		if seen[version] {
			return nil, fmt.Errorf("version %s is claimed by more than one migration", version)
		}
		seen[version] = true

		body, err := fs.ReadFile(files, "migrations/"+entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		migrations = append(migrations, migration{version: version, body: string(body)})
	}

	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("read applied migrations: %w", err)
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func applyOne(ctx context.Context, db *sql.DB, migration migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", migration.version, err)
	}
	defer func() { _ = tx.Rollback() }()

	if strings.TrimSpace(migration.body) != "" {
		if _, err := tx.ExecContext(ctx, migration.body); err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.version, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		migration.version,
	); err != nil {
		return fmt.Errorf("record migration %s: %w", migration.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", migration.version, err)
	}
	return nil
}
