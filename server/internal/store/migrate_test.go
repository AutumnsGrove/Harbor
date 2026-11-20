package store

import (
	"context"
	"database/sql"
	"fmt"
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
	if err := db.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&appliedAt); err != nil {
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

	if n := tableCount(t, db, "schema_migrations"); n != 0 {
		t.Errorf("a failed first migration left schema_migrations behind (%d)", n)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 0 {
		t.Errorf("user_version = %d, %v after a failed migration, want 0", version, err)
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
	migrations, err := readMigrations(migrationsFS())
	if err != nil {
		t.Fatalf("readMigrations() error = %v", err)
	}
	if len(migrations) == 0 || migrations[0].version != 1 || migrations[0].name != "init" {
		t.Fatalf("shipped migrations = %+v, want 0001_init first", migrations)
	}
}

// plans/02 test 4: PRAGMA user_version is the fast check, schema_migrations the
// history. They must move together.
func TestMigrateKeepsUserVersionAndHistoryTogether(t *testing.T) {
	db := rawDB(t)
	files := fstest.MapFS{
		"migrations/0001_first.sql":  {Data: []byte(`CREATE TABLE things (name TEXT);`)},
		"migrations/0002_second.sql": {Data: []byte(`ALTER TABLE things ADD COLUMN size INTEGER;`)},
	}
	if err := migrate(context.Background(), db, files); err != nil {
		t.Fatal(err)
	}

	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Errorf("user_version = %d, want 2", version)
	}
	rows, err := db.Query(`SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v int
		var name string
		if err := rows.Scan(&v, &name); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%d:%s", v, name))
	}
	if strings.Join(got, ",") != "1:first,2:second" {
		t.Errorf("history = %v, want [1:first 2:second]", got)
	}
}

func TestMigrateRollsBackUserVersionWithAFailedMigration(t *testing.T) {
	db := rawDB(t)
	files := fstest.MapFS{
		"migrations/0001_ok.sql":     {Data: []byte(`CREATE TABLE things (name TEXT);`)},
		"migrations/0002_broken.sql": {Data: []byte(`ALTER TABLE things ADD COLUMN a; NOT SQL;`)},
	}
	if err := migrate(context.Background(), db, files); err == nil {
		t.Fatal("migrate() error = nil, want the broken migration reported")
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Errorf("user_version = %d after a failed 0002, want 1", version)
	}
}

// design.md §7: numbering is strictly sequential in merge order, so a gap means
// a migration went missing.
func TestMigrateRejectsAGapInTheSequence(t *testing.T) {
	files := fstest.MapFS{
		"migrations/0001_a.sql": {Data: []byte("SELECT 1;")},
		"migrations/0003_c.sql": {Data: []byte("SELECT 1;")},
	}
	err := migrate(context.Background(), rawDB(t), files)
	if err == nil || !strings.Contains(err.Error(), "0002") {
		t.Fatalf("migrate() error = %v, want it to name the missing 0002", err)
	}
}

// Forward-only (design.md §3): a database written by a newer Harbor is refused,
// not downgraded or half-understood.
func TestMigrateRefusesADatabaseFromTheFuture(t *testing.T) {
	db := rawDB(t)
	if _, err := db.Exec(`PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{"migrations/0001_a.sql": {Data: []byte("SELECT 1;")}}
	err := migrate(context.Background(), db, files)
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("migrate() error = %v, want a refusal that says the database is newer", err)
	}
}

// Phase 0 shipped an empty migration set but its runner already created a
// schema_migrations table, with a TEXT version. The potato's database has it.
// Migration 0001 creates the design's shape, so the empty legacy table must be
// replaced, and a legacy table that holds rows must stop the boot.
func TestMigrateReplacesTheEmptyPhaseZeroHistoryTable(t *testing.T) {
	db := rawDB(t)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db, migrationsFS()); err != nil {
		t.Fatalf("migrate() over a phase-0 database error = %v", err)
	}
	var kind string
	if err := db.QueryRow(`SELECT type FROM pragma_table_info('schema_migrations') WHERE name = 'version'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "INTEGER" {
		t.Errorf("schema_migrations.version is %q, want INTEGER", kind)
	}
}

func TestMigrateRefusesALegacyHistoryTableWithRows(t *testing.T) {
	db := rawDB(t)
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
		INSERT INTO schema_migrations VALUES ('0001', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(context.Background(), db, migrationsFS()); err == nil {
		t.Fatal("migrate() error = nil, want a refusal rather than guessing at history")
	}
}

func tableCount(t *testing.T, db *sql.DB, name string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
