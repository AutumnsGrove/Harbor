package store

import (
	"context"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// plans/12a, "Tests first" 1: the export reads the whole library, and it reads
// it as one picture. A page-by-page walk of a live database can export an item
// whose tags were deleted a millisecond later, and a backup that disagrees with
// itself is worse than no backup.

func insertHighlight(t *testing.T, s *Store, id, itemID, quote string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO highlights (id, item_id, quote, note, color, anchor_json, detached)
		VALUES (?, ?, ?, '', 'amber', '{"quote":"x"}', 0)`, id, itemID, quote); err != nil {
		t.Fatalf("insert highlight: %v", err)
	}
}

func insertSpace(t *testing.T, s *Store, id, slug, name, kind string) {
	t.Helper()
	var slugArg any
	if slug != "" {
		slugArg = slug
	}
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO spaces (id, slug, name, kind, rule_json) VALUES (?, ?, ?, ?, '{}')`,
		id, slugArg, name, kind); err != nil {
		t.Fatalf("insert space: %v", err)
	}
}

func insertMedia(t *testing.T, s *Store, id, itemID, sha, path string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO media (id, item_id, kind, filename, content_type, bytes, sha256, local_path)
		VALUES (?, ?, 'image', 'cover.png', 'image/png', 1234, ?, ?)`,
		id, itemID, sha, path); err != nil {
		t.Fatalf("insert media: %v", err)
	}
}

func TestExportLibraryReadsEverything(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	item := mustCreate(t, s, ItemInput{
		ID: "a", URL: ptr("https://example.com/a"), Title: "An article",
		Summary: "It is about things.", Kind: model.KindArticle,
	})
	if _, err := s.SetItemTags(ctx, item.ID, []string{"Go", "sqlite"}, model.TagSourceAI); err != nil {
		t.Fatal(err)
	}
	note := mustCreate(t, s, ItemInput{ID: "b", Note: "A thought."})
	if err := s.SoftDeleteItem(ctx, note.ID); err != nil {
		t.Fatal(err)
	}

	insertSpace(t, s, "sp1", "", "Recipes", "manual")
	if _, err := s.db.ExecContext(ctx, `INSERT INTO space_items (space_id, item_id) VALUES ('sp1', 'a')`); err != nil {
		t.Fatal(err)
	}
	insertHighlight(t, s, "h1", "a", "the important sentence")
	insertMedia(t, s, "m1", "a", "abc123", "ab/abc123")

	library, err := s.ExportLibrary(ctx)
	if err != nil {
		t.Fatalf("ExportLibrary: %v", err)
	}

	// Every item, including the soft-deleted one: an export that silently drops
	// what the owner deleted is not the owner's library.
	if len(library.Items) != 2 {
		t.Fatalf("items = %d, want 2 (one live, one in recently deleted)", len(library.Items))
	}
	if got := library.Tags["a"]; len(got) != 2 || got[0] != "go" || got[1] != "sqlite" {
		t.Errorf("tags for a = %v, want go, sqlite", got)
	}
	// The seeded system Spaces and the owner's manual one all come out.
	seededCount := 2 // read-later (0001) and favourites (0002)
	if len(library.Spaces) != seededCount+1 {
		t.Fatalf("spaces = %+v, want %d seeded and one manual", library.Spaces, seededCount)
	}
	byID := map[string]model.Space{}
	for _, space := range library.Spaces {
		byID[space.ID] = space
	}
	if space, ok := byID["sp1"]; !ok || space.Name != "Recipes" || space.Slug != nil {
		t.Errorf("manual space = %+v", space)
	}
	var seeded *model.Space
	for i := range library.Spaces {
		if library.Spaces[i].Slug != nil && *library.Spaces[i].Slug == "read-later" {
			seeded = &library.Spaces[i]
		}
	}
	if seeded == nil {
		t.Errorf("the seeded read-later Space is missing from %+v", library.Spaces)
	}
	if space, ok := byID["favourites"]; !ok || space.Name != "Favourites" || space.Slug == nil || *space.Slug != "favourites" {
		t.Errorf("the seeded favourites Space is missing from %+v", library.Spaces)
	}
	if got := library.SpaceItems["sp1"]; len(got) != 1 || got[0] != "a" {
		t.Errorf("space items = %v", got)
	}
	if len(library.Highlights) != 1 || library.Highlights[0].Quote != "the important sentence" {
		t.Errorf("highlights = %+v", library.Highlights)
	}
	if len(library.Media) != 1 || library.Media[0].SHA256 != "abc123" {
		t.Errorf("media = %+v", library.Media)
	}
}

func TestExportLibraryIsOrdered(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "b", Note: "second"})
	mustCreate(t, s, ItemInput{ID: "a", Note: "first"})

	library, err := s.ExportLibrary(ctx)
	if err != nil {
		t.Fatalf("ExportLibrary: %v", err)
	}
	if len(library.Items) != 2 || library.Items[0].ID != "b" || library.Items[1].ID != "a" {
		t.Fatalf("items = %v, want insertion order (seq)", ids(library.Items))
	}
	// Deterministic order is what makes the export diffable and the manifest
	// reproducible, so it is a property of the query, not of the filesystem.
	if later, err := s.ExportLibrary(ctx); err != nil || !sameOrder(library, later) {
		t.Errorf("two reads disagreed: %v", err)
	}
}

func TestSchemaVersionIsReadable(t *testing.T) {
	s := openTestStore(t)
	version, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if want := shippedVersions(t); version != want {
		t.Errorf("version = %d, want %d (every shipped migration)", version, want)
	}
}

func TestExportLibraryOfAnEmptyLibraryIsNotAnError(t *testing.T) {
	library, err := openTestStore(t).ExportLibrary(context.Background())
	if err != nil {
		t.Fatalf("ExportLibrary: %v", err)
	}
	if len(library.Items) != 0 || library.Tags == nil {
		t.Errorf("library = %+v, want an empty one with an initialised map", library)
	}
}

func sameOrder(a, b model.Library) bool {
	if len(a.Items) != len(b.Items) {
		return false
	}
	for i := range a.Items {
		if a.Items[i].ID != b.Items[i].ID {
			return false
		}
	}
	return true
}
