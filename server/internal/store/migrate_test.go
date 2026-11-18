package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func rawDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), DBFile)
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMigrateAppliesInVersionOrder(t *testing.T) {
	db := rawDB(t)
	files := fstest.MapFS{
		"migrations/0002_second.sql": {Data: []byte(`INSERT INTO things (name) VALUES ('second');`)},
		"migrations/0001_first.sql":  {Data: []byte(`CREATE TABLE things (name TEXT); INSERT INTO things (name) VALUES ('first');`)},
	}

	if err := migrate(context.Background(), db, files); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}

	rows, err := db.Query(`SELECT name FROM things ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if strings.Join(got, ",") != "first,second" {
		t.Errorf("migrations ran in order %v, want [first second]", got)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := rawDB(t)
	files := fstest.MapFS{
		"migrations/0001_first.sql": {Data: []byte(`CREATE TABLE things (name TEXT); INSERT INTO things (name) VALUES ('once');`)},
	}

	for run := 0; run < 3; run++ {
		if err := migrate(context.Background(), db, files); err != nil {
			t.Fatalf("migrate() run %d error = %v", run+1, err)
		}
	}

	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM things`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("a migration ran %d times, want once", rows)
	}

	var recorded int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Errorf("schema_migrations holds %d rows, want 1", recorded)
	}

	var appliedAt string
	if err := db.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = '0001'`).Scan(&appliedAt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(appliedAt, "Z") {
		t.Errorf("applied_at = %q, want an RFC 3339 UTC timestamp", appliedAt)
	}
}

func TestMigrateRecordsNothingWhenAMigrationFails(t *testing.T) {
	db := rawDB(t)
	files := fstest.MapFS{
		"migrations/0001_broken.sql": {Data: []byte(`CREATE TABLE things (name TEXT); INVALID SQL HERE;`)},
	}

	err := migrate(context.Background(), db, files)
	if err == nil {
		t.Fatal("migrate() with broken SQL error = nil, want a rejection")
	}
	if !strings.Contains(err.Error(), "0001") {
		t.Errorf("migrate() error = %q, want it to name the migration", err)
	}

	var recorded int
	if err := db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 0 {
		t.Errorf("schema_migrations recorded %d failed migrations, want 0", recorded)
	}
	var table string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='things'`).Scan(&table); err == nil {
		t.Error("a failed migration left its table behind instead of rolling back")
	}
}

func TestMigrateRejectsUnexpectedNames(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{
			name:  "no number prefix",
			files: fstest.MapFS{"migrations/init.sql": {Data: []byte("SELECT 1;")}},
			want:  "not named",
		},
		{
			name:  "too short a number",
			files: fstest.MapFS{"migrations/1_init.sql": {Data: []byte("SELECT 1;")}},
			want:  "not named",
		},
		{
			name:  "upper case",
			files: fstest.MapFS{"migrations/0001_Init.sql": {Data: []byte("SELECT 1;")}},
			want:  "not named",
		},
		{
			name: "the same version twice",
			files: fstest.MapFS{
				"migrations/0001_one.sql": {Data: []byte("SELECT 1;")},
				"migrations/0001_two.sql": {Data: []byte("SELECT 2;")},
			},
			want: "more than one migration",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := migrate(context.Background(), rawDB(t), test.files)
			if err == nil {
				t.Fatal("migrate() error = nil, want a rejection")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("migrate() error = %q, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestShippedMigrationsLoad(t *testing.T) {
	// Phase 0 ships an empty set with a .gitkeep; the point of the test is that
	// the directory is readable and non-SQL files are ignored.
	migrations, err := readMigrations(migrationsFS())
	if err != nil {
		t.Fatalf("readMigrations() error = %v", err)
	}
	if len(migrations) != 0 {
		t.Errorf("the shipped set holds %d migrations, want 0 before phase 2", len(migrations))
	}
}
