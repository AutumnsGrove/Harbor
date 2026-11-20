package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// design.md §7 is the schema; 0001_init.sql is that DDL verbatim. These tests
// are plans/02 "Tests first" 4, 5, 6 (cascades) and 7.

func exec(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", strings.Fields(query)[0], err)
	}
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// normalise reduces SQL to comparable statements: comments gone, whitespace
// collapsed, one statement per element.
func normalise(sqlText string) []string {
	noComments := regexp.MustCompile(`(?m)--.*$`).ReplaceAllString(sqlText, "")
	var out []string
	for _, stmt := range strings.Split(noComments, ";") {
		if fields := strings.Fields(stmt); len(fields) > 0 {
			out = append(out, strings.Join(fields, " "))
		}
	}
	return out
}

// The migration must be the design's DDL, not a paraphrase of it: a half-copied
// or "improved" schema is worse than none.
func TestMigration0001IsDesignSection7Verbatim(t *testing.T) {
	design, err := os.ReadFile("../../../docs/design.md")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(design), "## 7. Data model")
	fence := strings.Index(string(design)[start:], "```sql\n") + start + len("```sql\n")
	end := strings.Index(string(design)[fence:], "\n```") + fence
	want := normalise(string(design)[fence:end])

	migration, err := os.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	got := normalise(string(migration))

	// The one addition is the seeded system space (plans/02 "Data model notes").
	if n := len(got); n == 0 || !strings.HasPrefix(got[n-1], "INSERT INTO spaces") {
		t.Fatalf("0001 must end with the read-later seed, got last statement %q", got[len(got)-1])
	}
	got = got[:len(got)-1]

	if len(got) != len(want) {
		t.Fatalf("0001 has %d statements, design §7 has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("statement %d differs\n design: %s\n 0001:   %s", i+1, want[i], got[i])
		}
	}
}

func TestEmptyDatabaseGetsEveryTableAndIndex(t *testing.T) {
	s := openTestStore(t)

	tables := []string{
		"items", "tags", "tag_aliases", "item_tags", "media", "highlights", "spaces",
		"space_items", "space_suggestions", "archives", "items_fts", "item_embeddings",
		"processed_jobs", "import_runs", "task_runs", "schema_migrations",
	}
	for _, name := range tables {
		if count(t, s.db, `SELECT count(*) FROM sqlite_master WHERE name = ?`, name) != 1 {
			t.Errorf("table %s is missing", name)
		}
	}
	// FTS5 shadow tables prove the virtual table really is FTS5.
	for _, shadow := range []string{"items_fts_data", "items_fts_idx", "items_fts_docsize", "items_fts_config"} {
		if count(t, s.db, `SELECT count(*) FROM sqlite_master WHERE name = ?`, shadow) != 1 {
			t.Errorf("FTS5 shadow table %s is missing", shadow)
		}
	}
	indexes := []string{
		"items_by_url", "items_by_saved", "items_queue", "item_tags_by_tag", "media_by_item",
		"media_by_sha", "highlights_by_item", "space_items_by_item",
	}
	for _, name := range indexes {
		if count(t, s.db, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, name) != 1 {
			t.Errorf("index %s is missing", name)
		}
	}
	if got := count(t, s.db, `SELECT user_version FROM pragma_user_version`); got != 1 {
		t.Errorf("user_version = %d, want 1", got)
	}
}

func TestReadLaterIsSeeded(t *testing.T) {
	s := openTestStore(t)
	var name, kind string
	err := s.db.QueryRow(`SELECT name, kind FROM spaces WHERE slug = 'read-later'`).Scan(&name, &kind)
	if err != nil {
		t.Fatalf("read-later space: %v", err)
	}
	if name != "Read later" || kind != "manual" {
		t.Errorf("read-later = %q/%q, want Read later/manual", name, kind)
	}
}

func TestOpeningTwiceChangesNothing(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, first, `INSERT INTO items (id, url) VALUES ('a', 'https://example.com/')`)
	_ = first.Close()

	second, err := Open(dir)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()
	if n := count(t, second.db, `SELECT count(*) FROM schema_migrations`); n != 1 {
		t.Errorf("history holds %d rows after reopening, want 1", n)
	}
	if n := count(t, second.db, `SELECT count(*) FROM items`); n != 1 {
		t.Errorf("reopening changed the library: %d items, want 1", n)
	}
	if n := count(t, second.db, `SELECT count(*) FROM spaces WHERE slug = 'read-later'`); n != 1 {
		t.Errorf("reopening re-seeded read-later: %d rows", n)
	}
}

// plans/02 test 4: "an old user_version upgrades and preserves rows".
func TestAnOlderDatabaseUpgradesAndKeepsItsRows(t *testing.T) {
	path := t.TempDir() + "/" + DBFile
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := migrate(context.Background(), db, migrationsFS()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO items (id, url, title) VALUES ('keep', 'https://example.com/', 'kept')`); err != nil {
		t.Fatal(err)
	}

	next := fstest.MapFS{}
	real, err := readMigrations(migrationsFS())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range real {
		next[fmt.Sprintf("migrations/%04d_%s.sql", m.version, m.name)] = &fstest.MapFile{Data: []byte(m.body)}
	}
	next["migrations/0002_future.sql"] = &fstest.MapFile{Data: []byte(`ALTER TABLE items ADD COLUMN future TEXT NOT NULL DEFAULT 'x';`)}

	if err := migrate(context.Background(), db, next); err != nil {
		t.Fatalf("upgrade error = %v", err)
	}
	var title, future string
	if err := db.QueryRow(`SELECT title, future FROM items WHERE id = 'keep'`).Scan(&title, &future); err != nil {
		t.Fatal(err)
	}
	if title != "kept" || future != "x" {
		t.Errorf("row after upgrade = %q/%q, want kept/x", title, future)
	}
	if v := count(t, db, `SELECT user_version FROM pragma_user_version`); v != 2 {
		t.Errorf("user_version = %d, want 2", v)
	}
}

// plans/02 test 5 — design §7: the unique URL index.
func TestDuplicateCanonicalURLIsRejectedButNotesAreFree(t *testing.T) {
	s := openTestStore(t)
	exec(t, s, `INSERT INTO items (id, url) VALUES ('a', 'https://example.com/a')`)

	if _, err := s.db.Exec(`INSERT INTO items (id, url) VALUES ('b', 'https://example.com/a')`); err == nil {
		t.Error("a second item with the same canonical URL was accepted")
	}
	for i := 0; i < 5; i++ {
		exec(t, s, `INSERT INTO items (id, kind) VALUES (?, 'note')`, fmt.Sprintf("n%d", i))
	}
	for i := 0; i < 3; i++ { // the index also exempts the empty string
		exec(t, s, `INSERT INTO items (id, url) VALUES (?, '')`, fmt.Sprintf("e%d", i))
	}
	if n := count(t, s.db, `SELECT count(*) FROM items WHERE url IS NULL`); n != 5 {
		t.Errorf("%d NULL-URL notes stored, want 5", n)
	}
}

// design §5: the unique index deliberately includes soft-deleted rows, so a
// re-save restores the item instead of creating a second one.
func TestSoftDeletedURLStillBlocksADuplicate(t *testing.T) {
	s := openTestStore(t)
	exec(t, s, `INSERT INTO items (id, url, deleted_at) VALUES ('a', 'https://example.com/a', CURRENT_TIMESTAMP)`)
	if _, err := s.db.Exec(`INSERT INTO items (id, url) VALUES ('b', 'https://example.com/a')`); err == nil {
		t.Error("a soft-deleted URL did not block a duplicate")
	}
}

// AUTOINCREMENT is what keeps an FTS rowid from ever pointing at a new item.
func TestSeqIsNeverReused(t *testing.T) {
	s := openTestStore(t)
	exec(t, s, `INSERT INTO items (id) VALUES ('a')`)
	var first int
	if err := s.db.QueryRow(`SELECT seq FROM items WHERE id = 'a'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	exec(t, s, `DELETE FROM items WHERE id = 'a'`)
	exec(t, s, `INSERT INTO items (id) VALUES ('b')`)
	var second int
	if err := s.db.QueryRow(`SELECT seq FROM items WHERE id = 'b'`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Errorf("seq %d reused after deleting seq %d", second, first)
	}
}

// design §7 "Foreign keys everywhere": a purge is one DELETE on items, and it
// takes every child row with it and nothing else.
func TestDeletingAnItemCascadesToEveryChild(t *testing.T) {
	s := openTestStore(t)
	for _, id := range []string{"gone", "stay"} {
		exec(t, s, `INSERT INTO items (id) VALUES (?)`, id)
		exec(t, s, `INSERT INTO tags (name) VALUES (?)`, "tag-"+id)
		exec(t, s, `INSERT INTO item_tags (item_id, tag_id) SELECT ?, id FROM tags WHERE name = ?`, id, "tag-"+id)
		exec(t, s, `INSERT INTO media (id, item_id, kind, sha256, local_path) VALUES (?, ?, 'image', 'abc', 'p')`, "m-"+id, id)
		exec(t, s, `INSERT INTO highlights (id, item_id, quote, anchor_json) VALUES (?, ?, 'q', '{}')`, "h-"+id, id)
		exec(t, s, `INSERT INTO space_items (space_id, item_id) VALUES ('read-later', ?)`, id)
		exec(t, s, `INSERT INTO archives (item_id) VALUES (?)`, id)
		exec(t, s, `INSERT INTO item_embeddings (item_id, model, dims, content_hash, vec) VALUES (?, 'm', 1, 'h', x'00')`, id)
	}

	exec(t, s, `DELETE FROM items WHERE id = 'gone'`)

	children := map[string]string{
		"item_tags": "item_id", "media": "item_id", "highlights": "item_id",
		"space_items": "item_id", "archives": "item_id", "item_embeddings": "item_id",
	}
	for table, col := range children {
		if n := count(t, s.db, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s = 'gone'`, table, col)); n != 0 {
			t.Errorf("%s still holds %d rows of the purged item", table, n)
		}
		if n := count(t, s.db, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s = 'stay'`, table, col)); n != 1 {
			t.Errorf("%s holds %d rows of the other item, want 1", table, n)
		}
	}
	if n := count(t, s.db, `SELECT count(*) FROM spaces WHERE slug = 'read-later'`); n != 1 {
		t.Error("purging an item removed the Read later space")
	}
}

func TestDeletingATagCascadesToItsAliasesAndLinks(t *testing.T) {
	s := openTestStore(t)
	exec(t, s, `INSERT INTO items (id) VALUES ('a')`)
	exec(t, s, `INSERT INTO tags (id, name) VALUES (1, 'go')`)
	exec(t, s, `INSERT INTO tag_aliases (alias, tag_id) VALUES ('golang', 1)`)
	exec(t, s, `INSERT INTO item_tags (item_id, tag_id) VALUES ('a', 1)`)
	exec(t, s, `DELETE FROM tags WHERE id = 1`)
	if count(t, s.db, `SELECT count(*) FROM tag_aliases`) != 0 || count(t, s.db, `SELECT count(*) FROM item_tags`) != 0 {
		t.Error("deleting a tag left aliases or item links behind")
	}
}

func TestDeletingASpaceCascadesToMembership(t *testing.T) {
	s := openTestStore(t)
	exec(t, s, `INSERT INTO items (id) VALUES ('a')`)
	exec(t, s, `INSERT INTO space_items (space_id, item_id) VALUES ('read-later', 'a')`)
	exec(t, s, `DELETE FROM spaces WHERE slug = 'read-later'`)
	if n := count(t, s.db, `SELECT count(*) FROM space_items`); n != 0 {
		t.Errorf("deleting a space left %d memberships", n)
	}
}

func TestForeignKeysRejectOrphans(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO media (id, item_id, kind, sha256, local_path) VALUES ('m', 'nobody', 'image', 'x', 'p')`); err == nil {
		t.Error("a media row for a missing item was accepted")
	}
}

// ftsHits returns the items whose indexed text matches query.
func ftsHits(t *testing.T, s *Store, query string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT i.id FROM items_fts f JOIN items i ON i.seq = f.rowid WHERE items_fts MATCH ? ORDER BY i.id`, query)
	if err != nil {
		t.Fatalf("fts query %q: %v", query, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// plans/02 test 7 and design §7: items_fts is an external-content table
// written explicitly, and an update or delete must first issue the FTS 'delete'
// command with the OLD column values, tags_text included. This is the recipe
// the store's item writes will follow.
func TestFTSInsertUpdateDeleteReflect(t *testing.T) {
	s := openTestStore(t)

	insert := func(id, title, note, tags string) int64 {
		res, err := s.db.Exec(`INSERT INTO items (id, title, note, tags_text) VALUES (?, ?, ?, ?)`, id, title, note, tags)
		if err != nil {
			t.Fatal(err)
		}
		seq, _ := res.LastInsertId()
		exec(t, s, `INSERT INTO items_fts (rowid, title, summary, content_text, transcript_text, note, tags_text)
			SELECT seq, title, summary, content_text, transcript_text, note, tags_text FROM items WHERE seq = ?`, seq)
		return seq
	}

	seq := insert("a", "Sourdough starter", "feed it daily", "baking bread")
	// A note-only item is findable by its note text (plans/02 test 7).
	if got := ftsHits(t, s, "daily"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("note text not indexed: %v", got)
	}
	if got := ftsHits(t, s, "baking"); len(got) != 1 {
		t.Fatalf("tags_text not indexed: %v", got)
	}

	// Update: delete the OLD values first, then write the new row.
	exec(t, s, `INSERT INTO items_fts (items_fts, rowid, title, summary, content_text, transcript_text, note, tags_text)
		SELECT 'delete', seq, title, summary, content_text, transcript_text, note, tags_text FROM items WHERE seq = ?`, seq)
	exec(t, s, `UPDATE items SET title = 'Rye starter', note = 'feed weekly', tags_text = 'fermenting' WHERE seq = ?`, seq)
	exec(t, s, `INSERT INTO items_fts (rowid, title, summary, content_text, transcript_text, note, tags_text)
		SELECT seq, title, summary, content_text, transcript_text, note, tags_text FROM items WHERE seq = ?`, seq)

	for _, gone := range []string{"sourdough", "daily", "baking"} {
		if got := ftsHits(t, s, gone); len(got) != 0 {
			t.Errorf("stale term %q still matches %v after the update", gone, got)
		}
	}
	for _, fresh := range []string{"rye", "weekly", "fermenting"} {
		if got := ftsHits(t, s, fresh); len(got) != 1 {
			t.Errorf("new term %q does not match after the update: %v", fresh, got)
		}
	}

	// Delete, again with the old values, tags_text included.
	exec(t, s, `INSERT INTO items_fts (items_fts, rowid, title, summary, content_text, transcript_text, note, tags_text)
		SELECT 'delete', seq, title, summary, content_text, transcript_text, note, tags_text FROM items WHERE seq = ?`, seq)
	exec(t, s, `DELETE FROM items WHERE seq = ?`, seq)
	for _, term := range []string{"rye", "weekly", "fermenting"} {
		if got := ftsHits(t, s, term); len(got) != 0 {
			t.Errorf("deleted item still matches %q: %v", term, got)
		}
	}
	// The index itself must still be sound after the round trip.
	if _, err := s.db.Exec(`INSERT INTO items_fts (items_fts) VALUES ('integrity-check')`); err != nil {
		t.Errorf("FTS integrity-check failed: %v", err)
	}
}
