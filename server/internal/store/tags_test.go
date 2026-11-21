package store

import (
	"context"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// plans/03 test 20 — the one tag write path. Normalise, resolve an alias left
// by a merge, reuse the canonical tag, and keep tags_text and FTS in the same
// transaction as the link.

func TestNormaliseTag(t *testing.T) {
	tests := map[string]string{
		"  Web   Dev ":            "web dev",
		"Rust":                    "rust",
		"\tWeb\nDev ":             "web dev",
		"":                        "",
		"   ":                     "",
		"Ünicode 🦀":               "ünicode 🦀",
		"C++":                     "c++",
		"web dev":                 "web dev",
		"WEB\t\tDEV":              "web dev",
		"non-breaking\u00a0space": "non-breaking space",
	}
	for input, want := range tests {
		if got := NormaliseTag(input); got != want {
			t.Errorf("NormaliseTag(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSetItemTagsNormalisesDeduplicatesAndIndexes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Note: "x"})

	canonical, err := s.SetItemTags(ctx, item.ID, []string{"  Web   Dev ", "web dev", "Rust", ""}, model.TagSourceAI)
	if err != nil {
		t.Fatalf("SetItemTags: %v", err)
	}
	want := []string{"rust", "web dev"}
	if len(canonical) != len(want) || canonical[0] != want[0] || canonical[1] != want[1] {
		t.Errorf("canonical tags = %v, want %v", canonical, want)
	}
	if n := count(t, s.db, `SELECT count(*) FROM tags`); n != 2 {
		t.Errorf("%d tag rows, want 2", n)
	}

	tags, err := s.TagsForItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("TagsForItem: %v", err)
	}
	if strings.Join(tags, ",") != "rust,web dev" {
		t.Errorf("tags = %v", tags)
	}
	if hits := ftsHits(t, s, "rust"); len(hits) != 1 || hits[0] != item.ID {
		t.Errorf("searching a tag found %v, want the item", hits)
	}
	if hits := ftsHits(t, s, `"web dev"`); len(hits) != 1 {
		t.Errorf("searching the multi-word tag found %v", hits)
	}
}

func TestSetItemTagsResolvesAnAlias(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", Note: "x"})
	if _, err := s.SetItemTags(ctx, "a", []string{"web dev"}, model.TagSourceManual); err != nil {
		t.Fatalf("SetItemTags: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO tag_aliases (alias, tag_id) SELECT 'webdev', id FROM tags WHERE name = 'web dev'`); err != nil {
		t.Fatalf("inserting the alias: %v", err)
	}

	canonical, err := s.SetItemTags(ctx, item.ID, []string{"WebDev"}, model.TagSourceAI)
	if err != nil {
		t.Fatalf("SetItemTags: %v", err)
	}
	if len(canonical) != 1 || canonical[0] != "web dev" {
		t.Errorf("canonical = %v, want the alias target 'web dev'", canonical)
	}
	if n := count(t, s.db, `SELECT count(*) FROM tags`); n != 1 {
		t.Errorf("%d tags after resolving an alias, want 1", n)
	}
}

func TestReplacingAITagsKeepsManualTags(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "a", Note: "x", Tags: []string{"Baking"}})

	if _, err := s.SetItemTags(ctx, "a", []string{"rust"}, model.TagSourceAI); err != nil {
		t.Fatalf("first SetItemTags: %v", err)
	}
	if _, err := s.SetItemTags(ctx, "a", []string{"go"}, model.TagSourceAI); err != nil {
		t.Fatalf("second SetItemTags: %v", err)
	}

	sources, err := s.TagsForItemWithSource(ctx, "a")
	if err != nil {
		t.Fatalf("TagsForItemWithSource: %v", err)
	}
	if sources["baking"] != model.TagSourceManual {
		t.Errorf("manual tag baking = %q, want manual", sources["baking"])
	}
	if sources["go"] != model.TagSourceAI {
		t.Errorf("go = %q, want ai", sources["go"])
	}
	if _, ok := sources["rust"]; ok {
		t.Error("the previous AI tag was not removed")
	}
}

func TestAnAITagNeverDowngradesAManualOne(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "a", Note: "x", Tags: []string{"baking"}})
	if _, err := s.SetItemTags(ctx, "a", []string{"baking"}, model.TagSourceAI); err != nil {
		t.Fatalf("SetItemTags: %v", err)
	}
	sources, err := s.TagsForItemWithSource(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if sources["baking"] != model.TagSourceManual {
		t.Errorf("source = %q, want manual to win", sources["baking"])
	}
}

func TestAnAliasCycleIsAnErrorNotALoop(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", Note: "x"})
	if _, err := s.db.ExecContext(ctx, `INSERT INTO tags (name) VALUES ('one'), ('two')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tag_aliases (alias, tag_id) SELECT 'one', id FROM tags WHERE name = 'two';
		INSERT INTO tag_aliases (alias, tag_id) SELECT 'two', id FROM tags WHERE name = 'one';`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemTags(ctx, item.ID, []string{"one"}, model.TagSourceAI); err == nil {
		t.Fatal("an alias cycle was accepted")
	}
}

func TestALongAliasChainIsCapped(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", Note: "x"})
	// Twelve tags, each an alias for the next: longer than maxAliasDepth.
	for n := 0; n < 12; n++ {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO tags (name) VALUES (?)`, "t"+strings.Repeat("x", n)); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n < 11; n++ {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO tag_aliases (alias, tag_id) SELECT ?, id FROM tags WHERE name = ?`,
			"t"+strings.Repeat("x", n), "t"+strings.Repeat("x", n+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetItemTags(ctx, item.ID, []string{"t"}, model.TagSourceAI); err == nil {
		t.Fatal("an eleven-hop alias chain was accepted")
	}
}

func TestTaggingASoftDeletedItemDoesNotMakeItSearchable(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "a", Note: "x"})
	if err := s.SoftDeleteItem(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetItemTags(ctx, "a", []string{"rust"}, model.TagSourceAI); err != nil {
		t.Fatalf("SetItemTags: %v", err)
	}
	if hits := ftsHits(t, s, "rust"); len(hits) != 0 {
		t.Errorf("a soft-deleted item is in FTS: %v", hits)
	}
	tags, err := s.TagsForItem(ctx, "a")
	if err != nil || len(tags) != 1 {
		t.Errorf("tags = %v, %v; want the write to persist", tags, err)
	}
}

func TestTagVocabularyIsOrderedByUseAndSkipsDeletedItems(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "a", Note: "x", Tags: []string{"rust"}})
	mustCreate(t, s, ItemInput{ID: "b", Note: "x", Tags: []string{"rust", "go"}})
	mustCreate(t, s, ItemInput{ID: "c", Note: "x", Tags: []string{"cobol"}})
	if err := s.SoftDeleteItem(ctx, "c"); err != nil {
		t.Fatal(err)
	}

	vocab, err := s.TagVocabulary(ctx, 10)
	if err != nil {
		t.Fatalf("TagVocabulary: %v", err)
	}
	if len(vocab) != 2 || vocab[0] != "rust" || vocab[1] != "go" {
		t.Errorf("vocab = %v, want rust then go", vocab)
	}
}
