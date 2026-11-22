package store

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// plans/05 test 10 and the S9 decision: favourites are a real thing, so the
// import phase seeds a manual Space the way 0001 seeded Read later.
func TestMigrationSeedsFavourites(t *testing.T) {
	s := openTestStore(t)

	var (
		name string
		kind string
	)
	if err := s.db.QueryRow(`SELECT name, kind FROM spaces WHERE slug = 'favourites'`).Scan(&name, &kind); err != nil {
		t.Fatalf("favourites space: %v", err)
	}
	if name != "Favourites" || kind != string(model.SpaceManual) {
		t.Errorf("favourites = %q/%q, want Favourites/manual", name, kind)
	}
}

// A collection name becomes a manual Space, and running the import twice must
// not make two Spaces out of one folder.
func TestEnsureManualSpaceReusesCaseInsensitively(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first, err := s.EnsureManualSpace(ctx, "Recipes")
	if err != nil {
		t.Fatalf("EnsureManualSpace: %v", err)
	}
	if first.Name != "Recipes" || first.Kind != model.SpaceManual || first.Slug != nil {
		t.Errorf("space = %+v, want a manual Space named Recipes with no slug", first)
	}

	for _, alias := range []string{"recipes", "  RECIPES  ", "Recipes"} {
		again, err := s.EnsureManualSpace(ctx, alias)
		if err != nil {
			t.Fatalf("EnsureManualSpace(%q): %v", alias, err)
		}
		if again.ID != first.ID {
			t.Errorf("EnsureManualSpace(%q) = %q, want the same Space as %q", alias, again.ID, first.ID)
		}
	}
	if n := count(t, s.db, `SELECT count(*) FROM spaces WHERE lower(name) = 'recipes'`); n != 1 {
		t.Errorf("%d Recipes spaces exist, want 1", n)
	}
}

// The seeded favourites Space is found by its name, not duplicated by an import
// that also has a "Favourites" folder.
func TestEnsureManualSpaceFindsTheSeededFavourites(t *testing.T) {
	s := openTestStore(t)
	space, err := s.EnsureManualSpace(context.Background(), "favourites")
	if err != nil {
		t.Fatalf("EnsureManualSpace: %v", err)
	}
	if space.ID != "favourites" {
		t.Errorf("space = %+v, want the seeded favourites row", space)
	}
}

// A rule-driven Space that happens to share the folder's name is not the same
// thing: putting items in it by hand would contradict its rule.
func TestEnsureManualSpaceIgnoresRuleSpaces(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO spaces (id, name, kind, rule_json) VALUES ('auto1', 'Reading', 'auto', '{}')`); err != nil {
		t.Fatal(err)
	}
	space, err := s.EnsureManualSpace(context.Background(), "Reading")
	if err != nil {
		t.Fatalf("EnsureManualSpace: %v", err)
	}
	if space.ID == "auto1" {
		t.Fatal("EnsureManualSpace reused a rule-driven Space")
	}
	if space.Kind != model.SpaceManual {
		t.Errorf("kind = %q, want manual", space.Kind)
	}
}

// The dry run asks whether a folder would create a Space, so asking must never
// create one.
func TestFindManualSpaceWritesNothing(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, ok, err := s.FindManualSpace(ctx, "Recipes"); err != nil || ok {
		t.Fatalf("FindManualSpace before any import = %v, %v; want not found", ok, err)
	}
	if n := count(t, s.db, `SELECT count(*) FROM spaces`); n != 2 {
		t.Fatalf("%d spaces after a read, want the 2 seeded ones", n)
	}

	created, err := s.EnsureManualSpace(ctx, "Recipes")
	if err != nil {
		t.Fatal(err)
	}
	space, ok, err := s.FindManualSpace(ctx, "recipes")
	if err != nil || !ok {
		t.Fatalf("FindManualSpace after creating = %v, %v; want found", ok, err)
	}
	if space.ID != created.ID {
		t.Errorf("found %q, want %q", space.ID, created.ID)
	}
	if _, ok, err := s.FindManualSpace(ctx, "  "); err != nil || ok {
		t.Errorf("a blank name returned %v, %v; want not found", ok, err)
	}
}

func TestEnsureManualSpaceRejectsAnEmptyName(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.EnsureManualSpace(context.Background(), "   "); err == nil {
		t.Fatal("EnsureManualSpace accepted a blank name")
	}
}

func TestAddItemToSpaceIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	space, err := s.EnsureManualSpace(ctx, "Recipes")
	if err != nil {
		t.Fatal(err)
	}
	item := mustCreate(t, s, ItemInput{ID: "a", Title: "Sourdough"})

	for i := 0; i < 3; i++ {
		if err := s.AddItemToSpace(ctx, space.ID, item.ID); err != nil {
			t.Fatalf("AddItemToSpace #%d: %v", i+1, err)
		}
	}
	if n := count(t, s.db, `SELECT count(*) FROM space_items WHERE space_id = ?`, space.ID); n != 1 {
		t.Errorf("%d memberships, want 1", n)
	}
}

// Failing loudly is the point: the driver counts an error, and a silently
// unfiled item is worse than a reported one.
func TestAddItemToSpaceRejectsUnknownIDs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	space, err := s.EnsureManualSpace(ctx, "Recipes")
	if err != nil {
		t.Fatal(err)
	}
	item := mustCreate(t, s, ItemInput{ID: "a", Title: "Sourdough"})

	err = s.AddItemToSpace(ctx, space.ID, "nope")
	if err == nil {
		t.Error("AddItemToSpace accepted an unknown item")
	} else if !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "item") {
		t.Errorf("the error should name the unknown item: %v", err)
	}
	err = s.AddItemToSpace(ctx, "nope", item.ID)
	if err == nil {
		t.Error("AddItemToSpace accepted an unknown Space")
	} else if !strings.Contains(err.Error(), `"nope"`) || !strings.Contains(err.Error(), "Space") {
		t.Errorf("the error should name the unknown Space: %v", err)
	}
}

// Imported highlights keep the cell's order and are idempotent: a second import
// of the same file must not duplicate them, and must not rewrite the rows it
// already wrote (the created_at stamps are part of the library).
func TestReplaceImportedHighlightsIsIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Title: "A"})

	quotes := []string{"first passage", "second passage", "third passage"}
	if err := s.ReplaceImportedHighlights(ctx, item.ID, quotes); err != nil {
		t.Fatalf("ReplaceImportedHighlights: %v", err)
	}
	first, err := s.HighlightsForItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("highlights = %d, want 3", len(first))
	}
	for i, h := range first {
		if h.Quote != quotes[i] {
			t.Errorf("highlight %d = %q, want %q (order is the cell's)", i, h.Quote, quotes[i])
		}
		if h.Note != "" {
			t.Errorf("highlight %d note = %q, want empty", i, h.Note)
		}
		if h.Color != model.ColorAmber {
			t.Errorf("highlight %d colour = %q, want the default", i, h.Color)
		}
		if !strings.Contains(h.AnchorJSON, `"source":"imported"`) {
			t.Errorf("highlight %d anchor = %q, want an imported marker", i, h.AnchorJSON)
		}
		if !strings.Contains(h.AnchorJSON, `"index":`+strconv.Itoa(i)) {
			t.Errorf("highlight %d anchor = %q, want its index", i, h.AnchorJSON)
		}
	}

	if err := s.ReplaceImportedHighlights(ctx, item.ID, quotes); err != nil {
		t.Fatalf("second ReplaceImportedHighlights: %v", err)
	}
	second, err := s.HighlightsForItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != len(first) {
		t.Fatalf("a second import made %d highlights, want %d", len(second), len(first))
	}
	for i := range first {
		if second[i].ID != first[i].ID || !second[i].CreatedAt.Equal(first[i].CreatedAt) {
			t.Errorf("highlight %d was rewritten: %+v then %+v", i, first[i], second[i])
		}
	}
}

// A shorter cell replaces a longer one: the file is the truth for imported rows.
func TestReplaceImportedHighlightsReplaces(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Title: "A"})

	if err := s.ReplaceImportedHighlights(ctx, item.ID, []string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceImportedHighlights(ctx, item.ID, []string{"only"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.HighlightsForItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Quote != "only" {
		t.Errorf("highlights = %+v, want just the new one", got)
	}
}

// The owner's own highlights are not the file's to delete.
func TestReplaceImportedHighlightsKeepsReaderHighlights(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Title: "A"})

	if _, err := s.db.Exec(`INSERT INTO highlights (id, item_id, quote, anchor_json) VALUES ('h1', ?, 'mine', '{"source":"reader","exact":"mine"}')`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceImportedHighlights(ctx, item.ID, []string{"imported"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceImportedHighlights(ctx, item.ID, nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.HighlightsForItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "h1" {
		t.Errorf("highlights = %+v, want only the reader's", got)
	}
}

func TestReplaceImportedHighlightsSkipsBlankQuotes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Title: "A"})

	if err := s.ReplaceImportedHighlights(ctx, item.ID, []string{"", "  ", "real"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.HighlightsForItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Quote != "real" {
		t.Errorf("highlights = %+v, want only the real quote", got)
	}
}

// The lookup and the insert are one transaction, so concurrent callers cannot
// both decide "no such Space" and both create it. The pool is a single
// connection, which is exactly why the pair has to be a transaction: statements
// serialise, the read-then-write does not.
func TestEnsureManualSpaceConcurrentCallsMakeOneSpace(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	const callers = 8
	start := make(chan struct{})
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			space, err := s.EnsureManualSpace(ctx, "Recipes")
			if err != nil {
				errs <- err
				return
			}
			ids <- space.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		t.Fatalf("EnsureManualSpace: %v", err)
	}
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Errorf("callers got %d different Spaces, want 1: %v", len(seen), seen)
	}
	if n := count(t, s.db, `SELECT count(*) FROM spaces WHERE lower(name) = 'recipes'`); n != 1 {
		t.Errorf("%d Recipes spaces exist, want 1", n)
	}
}
