package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// plans/02 test 16 at the store level: the enrichment queue is priority first,
// then due time, then age.
func TestQueuedItemsOrdersByPriorityThenDue(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	mustCreate(t, s, ItemInput{ID: "low", Note: "import backlog", Priority: -1, SavedAt: base})
	mustCreate(t, s, ItemInput{ID: "high", Note: "interactive", Priority: 0, SavedAt: base.Add(time.Hour)})
	mustCreate(t, s, ItemInput{ID: "blocked", Note: "waiting", Priority: 0, SavedAt: base.Add(2 * time.Hour)})

	if _, err := s.db.ExecContext(ctx,
		`UPDATE items SET next_attempt_at = ? WHERE id = 'blocked'`,
		stamp(base.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}

	items, err := s.QueuedItems(ctx, 10)
	if err != nil {
		t.Fatalf("QueuedItems: %v", err)
	}
	want := []string{"high", "blocked", "low"}
	if len(items) != len(want) {
		t.Fatalf("QueuedItems returned %v, want %v", ids(items), want)
	}
	for i, id := range want {
		if items[i].ID != id {
			t.Errorf("position %d = %q, want %q (full order %v)", i, items[i].ID, id, ids(items))
		}
	}
}

func TestQueuedItemsSkipsWorkThatIsNotDueYet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "due", Note: "now"})
	mustCreate(t, s, ItemInput{ID: "later", Note: "after the backoff"})
	if _, err := s.db.ExecContext(ctx,
		`UPDATE items SET next_attempt_at = ? WHERE id = 'later'`,
		stamp(time.Now().UTC().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	items, err := s.QueuedItems(ctx, 10)
	if err != nil {
		t.Fatalf("QueuedItems: %v", err)
	}
	if len(items) != 1 || items[0].ID != "due" {
		t.Errorf("QueuedItems = %v, want only the due item", ids(items))
	}
}

func TestRecordEnrichmentFailureAdvancesTheRetryColumns(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "a", Note: "x"})

	next := time.Now().UTC().Add(10 * time.Minute)
	item, err := s.RecordEnrichmentFailure(ctx, "a", "fetch failed", &next)
	if err != nil {
		t.Fatalf("RecordEnrichmentFailure: %v", err)
	}
	if item.Attempts != 1 || item.State != model.StateQueued || item.LastError != "fetch failed" || item.NextAttemptAt == nil {
		t.Errorf("after the first failure = %+v", item)
	}

	item, err = s.RecordEnrichmentFailure(ctx, "a", "gave up", nil)
	if err != nil {
		t.Fatalf("RecordEnrichmentFailure: %v", err)
	}
	if item.Attempts != 2 || item.State != model.StateFailed || item.NextAttemptAt != nil {
		t.Errorf("after the terminal failure = %+v", item)
	}
}

func TestMarkReadyIsIdempotentAndLeavesTheQueue(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "a", Note: "queued"})

	if err := s.MarkReady(ctx, "a"); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	item, err := s.GetItem(ctx, "a")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.State != "ready" || item.EnrichedAt == nil {
		t.Errorf("item after MarkReady = state %q, enrichedAt %v; want ready and set", item.State, item.EnrichedAt)
	}
	if err := s.MarkReady(ctx, "a"); err != nil {
		t.Errorf("second MarkReady: %v, want idempotent", err)
	}
	items, err := s.QueuedItems(ctx, 10)
	if err != nil {
		t.Fatalf("QueuedItems: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("QueuedItems still holds %v", ids(items))
	}

	// A soft-deleted queued item is not enrichment work.
	mustCreate(t, s, ItemInput{ID: "b", Note: "deleted before enrichment"})
	if err := s.SoftDeleteItem(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.QueuedItems(ctx, 10); len(items) != 0 {
		t.Errorf("QueuedItems returned a soft-deleted item: %v", ids(items))
	}
}

// plans/03 test 8's existing half plus the write path: one successful pass
// changes the row, its AI tags and the FTS entry together.
func TestApplyEnrichmentWritesTheRowAndItsTags(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{
		ID:          "a",
		URL:         ptr("https://example.com/a"),
		Title:       "https://example.com/a",
		Note:        "keep me",
		ContentText: "old content",
	})

	enriched, err := s.ApplyEnrichment(ctx, item.ID, Enrichment{
		Summary:        "A note about sourdough.",
		ContentText:    "new content about sourdough",
		ContentMD:      "## new content\n\nabout sourdough",
		DetailsJSON:    `{"type":"Recipe","name":"Bread"}`,
		OGJSON:         `{"title":"Bread"}`,
		Title:          "Bread",
		Kind:           model.KindRecipe,
		PromptVersion:  "tag.v1",
		ExtractVersion: "extract.v1",
		Tags:           []string{" Baking ", "bread"},
	})
	if err != nil {
		t.Fatalf("ApplyEnrichment: %v", err)
	}

	if enriched.Kind != model.KindRecipe || enriched.KindSource != model.KindSourceAI {
		t.Errorf("kind = %q/%q, want recipe/ai", enriched.Kind, enriched.KindSource)
	}
	if enriched.Title != "Bread" {
		t.Errorf("title = %q, want the placeholder replaced", enriched.Title)
	}
	if enriched.Summary != "A note about sourdough." || enriched.ContentMD == "" {
		t.Errorf("summary/markdown not written: %+v", enriched)
	}
	if enriched.State != model.StateReady || enriched.EnrichedAt == nil || enriched.Attempts != 0 {
		t.Errorf("state = %q, enrichedAt = %v, attempts = %d; want ready", enriched.State, enriched.EnrichedAt, enriched.Attempts)
	}
	tags, err := s.TagsForItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0] != "baking" || tags[1] != "bread" {
		t.Errorf("tags = %v, want baking and bread", tags)
	}

	if hits := ftsHits(t, s, "sourdough"); len(hits) != 1 {
		t.Errorf("the enriched text is not searchable: %v", hits)
	}
	if hits := ftsHits(t, s, "old"); len(hits) != 0 {
		t.Errorf("the old content_text is still indexed: %v", hits)
	}
	if hits := ftsHits(t, s, "keep"); len(hits) != 1 {
		t.Errorf("the owner's note stopped being searchable: %v", hits)
	}
}

func TestApplyEnrichmentNeverOverwritesAManualKind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a")})
	video := model.KindVideo
	if _, err := s.UpdateItem(ctx, item.ID, ItemPatch{Kind: &video}); err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}

	enriched, err := s.ApplyEnrichment(ctx, item.ID, Enrichment{
		Kind: model.KindRecipe, KindSource: model.KindSourceAI,
		Summary: "still enriched", Tags: []string{"bread"},
	})
	if err != nil {
		t.Fatalf("ApplyEnrichment: %v", err)
	}
	if enriched.Kind != model.KindVideo || enriched.KindSource != model.KindSourceManual {
		t.Errorf("kind = %q/%q, want video/manual preserved", enriched.Kind, enriched.KindSource)
	}
	if enriched.Summary != "still enriched" {
		t.Errorf("summary = %q, want the rest of enrichment to land", enriched.Summary)
	}
}

func TestApplyEnrichmentOnlyReplacesAPlaceholderTitle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	placeholder := mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Title: "https://example.com/a"})
	typed := mustCreate(t, s, ItemInput{ID: "b", URL: ptr("https://example.com/b"), Title: "My own title"})

	if _, err := s.ApplyEnrichment(ctx, placeholder.ID, Enrichment{Title: "Cleaned", Kind: model.KindArticle}); err != nil {
		t.Fatalf("ApplyEnrichment placeholder: %v", err)
	}
	if _, err := s.ApplyEnrichment(ctx, typed.ID, Enrichment{Title: "Cleaned", Kind: model.KindArticle}); err != nil {
		t.Fatalf("ApplyEnrichment typed: %v", err)
	}

	a, _ := s.GetItem(ctx, placeholder.ID)
	b, _ := s.GetItem(ctx, typed.ID)
	if a.Title != "Cleaned" {
		t.Errorf("placeholder title = %q, want the model's title", a.Title)
	}
	if b.Title != "My own title" {
		t.Errorf("typed title = %q, want it preserved", b.Title)
	}
}

func TestApplyEnrichmentKeepsManualTags(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", Note: "x", Tags: []string{"baking"}})
	if _, err := s.SetItemTags(ctx, item.ID, []string{"old-ai"}, model.TagSourceAI); err != nil {
		t.Fatal(err)
	}

	if _, err := s.ApplyEnrichment(ctx, item.ID, Enrichment{Kind: model.KindNote, Tags: []string{"rust"}}); err != nil {
		t.Fatalf("ApplyEnrichment: %v", err)
	}
	sources, err := s.TagsForItemWithSource(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sources["baking"] != model.TagSourceManual {
		t.Errorf("manual tag lost: %v", sources)
	}
	if sources["rust"] != model.TagSourceAI {
		t.Errorf("new AI tag missing: %v", sources)
	}
	if _, ok := sources["old-ai"]; ok {
		t.Errorf("the previous AI tag survived: %v", sources)
	}
}

func TestApplyEnrichmentRejectsAnInventedKind(t *testing.T) {
	s := openTestStore(t)
	item := mustCreate(t, s, ItemInput{ID: "a", Note: "x"})
	if _, err := s.ApplyEnrichment(context.Background(), item.ID, Enrichment{Kind: model.Kind("nonsense")}); err == nil {
		t.Fatal("an invalid kind was accepted")
	}
}

func TestSaveEmbeddingRoundTripsAndReplaces(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "a", Note: "x"})

	vec := make([]float32, 1024)
	vec[0], vec[512], vec[1023] = 0.25, -0.5, 1.5
	if err := s.SaveEmbedding(ctx, item.ID, "perplexity/pplx-embed-v1-0.6b", vec, "hash-one"); err != nil {
		t.Fatalf("SaveEmbedding: %v", err)
	}

	modelName, got, hash, err := s.GetEmbedding(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetEmbedding: %v", err)
	}
	if modelName != "perplexity/pplx-embed-v1-0.6b" || hash != "hash-one" || len(got) != 1024 {
		t.Fatalf("embedding = %q, %q, %d dims", modelName, hash, len(got))
	}
	if got[0] != 0.25 || got[512] != -0.5 || got[1023] != 1.5 {
		t.Errorf("vector changed in the round trip: %v %v %v", got[0], got[512], got[1023])
	}
	var dims int
	if err := s.db.QueryRowContext(ctx, `SELECT dims FROM item_embeddings WHERE item_id = 'a'`).Scan(&dims); err != nil || dims != 1024 {
		t.Errorf("stored dims = %d, %v; want 1024", dims, err)
	}

	if err := s.SaveEmbedding(ctx, item.ID, "new/model", []float32{0.1, 0.2, 0.3}, "hash-two"); err != nil {
		t.Fatalf("replacing the embedding: %v", err)
	}
	modelName, got, hash, err = s.GetEmbedding(ctx, item.ID)
	if err != nil || modelName != "new/model" || hash != "hash-two" || len(got) != 3 {
		t.Errorf("replaced embedding = %q, %q, %d (%v)", modelName, hash, len(got), err)
	}

	if err := s.SaveEmbedding(ctx, item.ID, "m", nil, "h"); err == nil {
		t.Error("an empty vector was accepted")
	}
	if _, _, _, err := s.GetEmbedding(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetEmbedding(missing) = %v, want ErrNotFound", err)
	}
}
