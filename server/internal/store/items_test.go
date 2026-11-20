package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// design.md §5 and §7 — the item row, its FTS row, and the merge rules. These
// are plans/02 "Tests first" 2, 5, 6, 15 at the package level: the store API the
// ingest path and the HTTP handlers both call.

func ptr(s string) *string { return &s }

// mustCreate is the boring wrapper that keeps the assertions readable.
func mustCreate(t *testing.T, s *Store, in ItemInput) model.Item {
	t.Helper()
	item, err := s.CreateItem(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateItem(%q): %v", in.Title, err)
	}
	return item
}

// design §7: the item and its FTS row are written in one transaction, and a
// note-only item is findable by its note text (plans/02 test 7).
func TestCreateItemWritesTheRowAndItsFTSEntry(t *testing.T) {
	s := openTestStore(t)
	item := mustCreate(t, s, ItemInput{
		ID:    "a",
		Title: "Sourdough starter",
		Note:  "feed it daily",
		Tags:  []string{"baking", "bread"},
	})

	if item.Seq == 0 {
		t.Error("Seq is 0; the row's FTS key would be useless")
	}
	if item.Kind != model.KindNote {
		t.Errorf("kind = %q, want note for a URL-less item (design §7)", item.Kind)
	}
	if item.State != model.StateQueued {
		t.Errorf("state = %q, want queued on insert", item.State)
	}
	if item.Source != model.SourceApp {
		t.Errorf("source = %q, want app by default", item.Source)
	}
	if got := ftsHits(t, s, "daily"); len(got) != 1 || got[0] != "a" {
		t.Errorf("note text not indexed: %v", got)
	}
	if got := ftsHits(t, s, "baking"); len(got) != 1 {
		t.Errorf("tags_text not indexed: %v", got)
	}
	if item.CreatedAt.IsZero() || item.SavedAt.IsZero() || item.UpdatedAt.IsZero() {
		t.Error("one of the three clocks is zero after insert")
	}
}

// plans/02 test 15: a link is an article until phase 3 classifies it, and never
// "unknown".
func TestCreateItemDefaultsAKindFromTheURL(t *testing.T) {
	s := openTestStore(t)
	item := mustCreate(t, s, ItemInput{ID: "link", URL: ptr("https://example.com/a")})
	if item.Kind != model.KindArticle {
		t.Errorf("kind = %q, want article", item.Kind)
	}
	if item.KindSource != model.KindSourceDefault {
		t.Errorf("kind_source = %q, want default", item.KindSource)
	}
	if item.URL == nil || *item.URL != "https://example.com/a" {
		t.Errorf("url = %v, want the canonical URL", item.URL)
	}
}

// design §7: a duplicate canonical URL is a constraint violation, not a second
// row — the merge rule has to be reached through MergeItem, never by inserting.
func TestCreateItemRejectsASecondCanonicalURL(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a")})
	if _, err := s.CreateItem(context.Background(), ItemInput{ID: "b", URL: ptr("https://example.com/a")}); err == nil {
		t.Fatal("a second item with the same canonical URL was accepted")
	}
	if n := count(t, s.db, `SELECT count(*) FROM items`); n != 1 {
		t.Errorf("%d items after the rejected insert, want 1", n)
	}
}

// plans/02 test 6: soft delete hides an item from list, search and get-live,
// but the row is still retrievable by id for the Recently deleted view.
func TestGetItemFindsASoftDeletedRow(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, ItemInput{ID: "a", Note: "gone soon"})
	if err := s.SoftDeleteItem(context.Background(), "a"); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	item, err := s.GetItem(context.Background(), "a")
	if err != nil {
		t.Fatalf("GetItem after soft delete: %v", err)
	}
	if item.DeletedAt == nil {
		t.Error("DeletedAt is nil; the item is not marked deleted")
	}
	if _, err := s.GetItem(context.Background(), "nobody"); err == nil {
		t.Error("GetItem on a missing id error = nil")
	}
}

// plans/02 test 13: live items ordered by saved_at newest first.
func TestListItemsOrdersBySavedAtAndHidesDeleted(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	mustCreate(t, s, ItemInput{ID: "old", Title: "old", SavedAt: base})
	mustCreate(t, s, ItemInput{ID: "new", Title: "new", SavedAt: base.Add(2 * time.Hour)})
	mustCreate(t, s, ItemInput{ID: "mid", Title: "mid", SavedAt: base.Add(time.Hour)})
	mustCreate(t, s, ItemInput{ID: "hidden", Title: "hidden", SavedAt: base.Add(3 * time.Hour)})
	if err := s.SoftDeleteItem(context.Background(), "hidden"); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListItems(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.ID)
	}
	want := []string{"new", "mid", "old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("list order = %v, want %v", got, want)
	}

	all, err := s.ListItems(context.Background(), ListOptions{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Errorf("IncludeDeleted returned %d items, want 4", len(all))
	}
}

// plans/02 test 13: the cursor is stable and total — no row is skipped and none
// appears twice, even when several items share a saved_at.
func TestListItemsPagesWithACursor(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"a", "b", "c", "d", "e"} {
		mustCreate(t, s, ItemInput{ID: id, Title: id, SavedAt: base.Add(time.Duration(i/2) * time.Minute)})
	}

	var seen []string
	var after *Cursor
	for page := 0; page < 10; page++ {
		items, err := s.ListItems(context.Background(), ListOptions{Limit: 2, After: after})
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			seen = append(seen, it.ID)
		}
		last := items[len(items)-1]
		after = &Cursor{SavedAt: last.SavedAt, Seq: last.Seq}
	}
	if len(seen) != 5 {
		t.Fatalf("paged over %d items, want 5: %v", len(seen), seen)
	}
	unique := map[string]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Errorf("item %q appeared on two pages", id)
		}
		unique[id] = true
	}
}

// design §7: updated_at tracks edits, and the FTS row is rewritten from the old
// values so a stale term stops matching (plans/02 test 7).
func TestUpdateItemBumpsUpdatedAtAndRewritesFTS(t *testing.T) {
	s := openTestStore(t)
	created := mustCreate(t, s, ItemInput{ID: "a", Title: "Sourdough", Note: "feed daily"})

	title, note := "Rye", "feed weekly"
	item, err := s.UpdateItem(context.Background(), "a", ItemPatch{Title: &title, Note: &note})
	if err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	if item.Title != "Rye" || item.Note != "feed weekly" {
		t.Errorf("update did not stick: %q/%q", item.Title, item.Note)
	}
	if !item.UpdatedAt.After(created.UpdatedAt) && !item.UpdatedAt.Equal(created.UpdatedAt) {
		t.Errorf("updated_at went backwards: %v -> %v", created.UpdatedAt, item.UpdatedAt)
	}
	if !item.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("created_at moved on an edit: %v -> %v", created.CreatedAt, item.CreatedAt)
	}
	for _, stale := range []string{"sourdough", "daily"} {
		if got := ftsHits(t, s, stale); len(got) != 0 {
			t.Errorf("stale term %q still matches %v", stale, got)
		}
	}
	for _, fresh := range []string{"rye", "weekly"} {
		if got := ftsHits(t, s, fresh); len(got) != 1 {
			t.Errorf("new term %q does not match: %v", fresh, got)
		}
	}

	if _, err := s.UpdateItem(context.Background(), "nobody", ItemPatch{Title: &title}); err == nil {
		t.Error("UpdateItem on a missing id error = nil")
	}
}

// plans/02 test 6: soft delete hides from list and search, restore brings it
// back, and neither touches the other rows.
func TestSoftDeleteRestoreRoundTrip(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, ItemInput{ID: "a", Note: "sourdough notes"})
	mustCreate(t, s, ItemInput{ID: "b", Note: "other notes"})

	if err := s.SoftDeleteItem(context.Background(), "a"); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	live, err := s.ListItems(context.Background(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].ID != "b" {
		t.Errorf("live list after delete = %v, want just b", live)
	}
	if got := ftsHits(t, s, "sourdough"); len(got) != 0 {
		t.Errorf("a soft-deleted item still matches search: %v", got)
	}
	if got := ftsHits(t, s, "other"); len(got) != 1 {
		t.Errorf("deleting one item disturbed another: %v", got)
	}

	if err := s.RestoreItem(context.Background(), "a"); err != nil {
		t.Fatalf("RestoreItem: %v", err)
	}
	live, err = s.ListItems(context.Background(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 {
		t.Errorf("%d live items after restore, want 2", len(live))
	}
	if got := ftsHits(t, s, "sourdough"); len(got) != 1 {
		t.Errorf("restored item is not searchable again: %v", got)
	}
}

// plans/02 test 6 and design §7 "a purge is one DELETE": the row and every
// child row go, and nothing else does.
func TestPurgeRemovesTheItemAndItsChildren(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, ItemInput{ID: "gone", Note: "purge me", Tags: []string{"x"}, ReadLater: true})
	mustCreate(t, s, ItemInput{ID: "stay", Note: "keep me", Tags: []string{"x"}})

	tagID := 0
	if err := s.db.QueryRow(`SELECT id FROM tags WHERE name = 'x'`).Scan(&tagID); err != nil {
		t.Fatalf("tag x: %v", err)
	}
	exec(t, s, `INSERT INTO media (id, item_id, kind, sha256, local_path) VALUES ('m', 'gone', 'image', 'abc', 'p')`)
	exec(t, s, `INSERT INTO highlights (id, item_id, quote, anchor_json) VALUES ('h', 'gone', 'q', '{}')`)

	if err := s.PurgeItem(context.Background(), "gone"); err != nil {
		t.Fatalf("PurgeItem: %v", err)
	}
	if n := count(t, s.db, `SELECT count(*) FROM items WHERE id = 'gone'`); n != 0 {
		t.Error("the item survived the purge")
	}
	for _, table := range []string{"media", "highlights", "item_tags", "space_items"} {
		if n := count(t, s.db, "SELECT count(*) FROM "+table+" WHERE item_id = 'gone'"); n != 0 {
			t.Errorf("%s still holds %d rows of the purged item", table, n)
		}
	}
	if n := count(t, s.db, `SELECT count(*) FROM items WHERE id = 'stay'`); n != 1 {
		t.Error("the purge removed the other item")
	}
	if got := ftsHits(t, s, "purge"); len(got) != 0 {
		t.Errorf("purged item still matches search: %v", got)
	}
	if err := s.PurgeItem(context.Background(), "nobody"); err == nil {
		t.Error("PurgeItem on a missing id error = nil")
	}
}

// design §5 — re-saving is a merge, not a duplicate: one row, the new note
// appended after the old, saved_at bumped and created_at kept.
func TestMergeItemAppendsTheNoteAndBumpsSavedAt(t *testing.T) {
	s := openTestStore(t)
	first := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	created := mustCreate(t, s, ItemInput{
		ID:          "a",
		URL:         ptr("https://example.com/a"),
		URLOriginal: "HTTPS://Example.com/a#frag",
		Title:       "",
		Note:        "first thought",
		CreatedAt:   first,
		SavedAt:     first,
	})

	item, merged, err := s.MergeItem(context.Background(), ItemInput{
		ID:          "new-id",
		URL:         ptr("https://example.com/a"),
		URLOriginal: "https://example.com/a?utm_source=x",
		Title:       "A real title",
		Note:        "second thought",
		SavedAt:     first.Add(48 * time.Hour),
	})
	if err != nil {
		t.Fatalf("MergeItem: %v", err)
	}
	if !merged {
		t.Fatal("merged = false; a re-save created a second item")
	}
	if item.ID != created.ID {
		t.Errorf("merge returned id %q, want the original %q", item.ID, created.ID)
	}
	if n := count(t, s.db, `SELECT count(*) FROM items`); n != 1 {
		t.Errorf("%d items after a re-save, want 1", n)
	}
	if !strings.HasPrefix(item.Note, "first thought") || !strings.HasSuffix(item.Note, "second thought") {
		t.Errorf("note = %q, want the old note then the new one", item.Note)
	}
	if !item.CreatedAt.Equal(first) {
		t.Errorf("created_at = %v, want the original %v", item.CreatedAt, first)
	}
	if !item.SavedAt.Equal(first.Add(48 * time.Hour)) {
		t.Errorf("saved_at = %v, want the re-save time", item.SavedAt)
	}
	if item.Title != "A real title" {
		t.Errorf("title = %q, want the refreshed metadata", item.Title)
	}
	if item.URLOriginal != "https://example.com/a?utm_source=x" {
		t.Errorf("url_original = %q, want this save's raw URL", item.URLOriginal)
	}
	// The merged note is what search has to see (plans/02 test 7).
	for _, term := range []string{"first", "second"} {
		if got := ftsHits(t, s, term); len(got) != 1 {
			t.Errorf("%q does not match after the merge: %v", term, got)
		}
	}
}

// design §5: a merge unions tags and keeps Read later membership.
func TestMergeItemUnionsTagsAndKeepsReadLater(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, ItemInput{
		ID: "a", URL: ptr("https://example.com/a"), Note: "one",
		Tags: []string{"go", "sqlite"}, ReadLater: true,
	})

	_, merged, err := s.MergeItem(context.Background(), ItemInput{
		ID: "b", URL: ptr("https://example.com/a"), Note: "two",
		Tags: []string{"sqlite", "raft"},
	})
	if err != nil || !merged {
		t.Fatalf("MergeItem: merged=%v err=%v", merged, err)
	}

	tags, err := s.TagsForItem(context.Background(), "a")
	if err != nil {
		t.Fatalf("TagsForItem: %v", err)
	}
	for _, want := range []string{"go", "raft", "sqlite"} {
		if !containsString(tags, want) {
			t.Errorf("tag %q missing after the merge: %v", want, tags)
		}
	}
	if len(tags) != 3 {
		t.Errorf("tags = %v, want exactly three", tags)
	}
	if !s.InReadLater(context.Background(), "a") {
		t.Error("the merge dropped Read later membership")
	}
}

// plans/02 test 2: appending the identical note twice is a no-op (compared on
// trimmed text), which is what stops a re-save from growing the note forever.
func TestMergeItemIgnoresAnIdenticalNote(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Note: "same words"})

	item, merged, err := s.MergeItem(context.Background(), ItemInput{
		ID: "b", URL: ptr("https://example.com/a"), Note: "  same words\n",
	})
	if err != nil || !merged {
		t.Fatalf("MergeItem: merged=%v err=%v", merged, err)
	}
	if strings.Count(item.Note, "same words") != 1 {
		t.Errorf("note = %q, want the duplicate suppressed", item.Note)
	}

	// An empty note on a re-save (a Refresh) must not append a separator.
	item, _, err = s.MergeItem(context.Background(), ItemInput{ID: "c", URL: ptr("https://example.com/a"), Title: "refreshed"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(strings.TrimSpace(item.Note), "---") {
		t.Errorf("an empty note left a dangling separator: %q", item.Note)
	}
}

// design §5: the unique index includes soft-deleted rows, so a re-save of a
// deleted URL restores that item and merges into it instead of creating a
// second one.
func TestMergeItemRestoresASoftDeletedItem(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Note: "before"})
	if err := s.SoftDeleteItem(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}

	item, merged, err := s.MergeItem(context.Background(), ItemInput{
		ID: "b", URL: ptr("https://example.com/a"), Note: "after",
	})
	if err != nil || !merged {
		t.Fatalf("MergeItem: merged=%v err=%v", merged, err)
	}
	if item.DeletedAt != nil {
		t.Error("the re-save did not restore the soft-deleted item")
	}
	if n := count(t, s.db, `SELECT count(*) FROM items`); n != 1 {
		t.Errorf("%d items, want 1", n)
	}
	live, err := s.ListItems(context.Background(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Errorf("restored item is not in the live list: %v", live)
	}
	if got := ftsHits(t, s, "after"); len(got) != 1 {
		t.Errorf("the restored item's new note is not searchable: %v", got)
	}
}

// plans/02 test 2 (rapid): merge order is stable under composition and
// re-merging is idempotent.
func TestMergeNoteOrderIsStable(t *testing.T) {
	noteGen := rapid.StringMatching(`[a-z]{1,12}`)
	rapid.Check(t, func(rt *rapid.T) {
		a, b, c := noteGen.Draw(rt, "a"), noteGen.Draw(rt, "b"), noteGen.Draw(rt, "c")
		s := openTestStore(t)
		ctx := context.Background()

		first, _, err := s.MergeItem(ctx, ItemInput{ID: "1", URL: ptr("https://example.com/x"), Note: a})
		if err != nil {
			rt.Fatalf("first merge: %v", err)
		}
		second, _, err := s.MergeItem(ctx, ItemInput{ID: "2", URL: ptr("https://example.com/x"), Note: b})
		if err != nil {
			rt.Fatalf("second merge: %v", err)
		}
		third, _, err := s.MergeItem(ctx, ItemInput{ID: "3", URL: ptr("https://example.com/x"), Note: c})
		if err != nil {
			rt.Fatalf("third merge: %v", err)
		}
		if first.ID != third.ID || second.ID != third.ID {
			rt.Fatalf("a merge created a new item: %s %s %s", first.ID, second.ID, third.ID)
		}

		// Order: a's text, then b's, then c's (accidental duplicates collapse).
		got := noteSegments(third.Note)
		want := dedupe([]string{a, b, c})
		if strings.Join(got, "|") != strings.Join(want, "|") {
			rt.Fatalf("note segments = %q, want %q (from %q)", got, want, third.Note)
		}

		// Re-merging the last note is a no-op.
		again, _, err := s.MergeItem(ctx, ItemInput{ID: "4", URL: ptr("https://example.com/x"), Note: c})
		if err != nil {
			rt.Fatalf("re-merge: %v", err)
		}
		if again.Note != third.Note {
			rt.Fatalf("re-merging the same note changed it:\n before %q\n after  %q", third.Note, again.Note)
		}
	})
}

// noteSegments splits a merged note back into its chunks, dropping the
// timestamp comments and the separator, so a property test can talk about order
// without tripping over letters inside a comment.
func noteSegments(note string) []string {
	var out []string
	for _, segment := range strings.Split(note, "---") {
		s := strings.TrimSpace(segment)
		for strings.HasPrefix(s, "<!--") {
			end := strings.Index(s, "-->")
			if end < 0 {
				break
			}
			s = strings.TrimSpace(s[end+3:])
		}
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s := strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func containsString(all []string, want string) bool {
	for _, got := range all {
		if got == want {
			return true
		}
	}
	return false
}
