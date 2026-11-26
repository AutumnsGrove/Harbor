package store

import (
	"context"
	"errors"
	"strings"
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
		Stamp(base.Add(-time.Hour))); err != nil {
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
		Stamp(time.Now().UTC().Add(time.Hour))); err != nil {
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

// A bulk import lands under --no-enrich, which leaves rows ready and unread by
// the model. QueueUnenriched is how that backlog becomes the enrichment loop's
// work (plans/05, "Volume vs the enrichment queue"), and it must touch nothing
// else: not an item the model already read, not one waiting in the queue, not a
// row in the bin.
func TestQueueUnenrichedQueuesOnlyTheFirstPass(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	imported := mustCreate(t, s, ItemInput{ID: "imported", Note: "from the export", SavedAt: base})
	older := mustCreate(t, s, ItemInput{ID: "older", Note: "also from the export", SavedAt: base.Add(-24 * time.Hour)})
	read := mustCreate(t, s, ItemInput{ID: "read", Note: "the model has seen this", SavedAt: base})
	queued := mustCreate(t, s, ItemInput{ID: "queued", Note: "already waiting", SavedAt: base})
	deleted := mustCreate(t, s, ItemInput{ID: "deleted", Note: "in the bin", SavedAt: base})

	// The import's own shape: ready, never enriched.
	for _, id := range []string{imported.ID, older.ID, deleted.ID} {
		if err := s.MarkReady(ctx, id); err != nil {
			t.Fatalf("MarkReady(%s): %v", id, err)
		}
	}
	// One row the model has read, and one still in the queue.
	if _, err := s.ApplyEnrichment(ctx, read.ID, Enrichment{
		Summary: "done", Kind: model.KindArticle, Tags: []string{"x"}, PromptVersion: "tag.v1",
	}); err != nil {
		t.Fatalf("ApplyEnrichment: %v", err)
	}
	if err := s.SoftDeleteItem(ctx, deleted.ID); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}

	// Two live rows have never met the model: the two imported ones. The
	// enriched row and the deleted one are not part of the backlog, and the
	// already-queued one is not waiting for its *first* pass.
	if n, err := s.UnenrichedCount(ctx); err != nil || n != 2 {
		t.Fatalf("UnenrichedCount = %d, %v; want 2", n, err)
	}

	queuedCount, remaining, err := s.QueueUnenriched(ctx, -1, 1)
	if err != nil {
		t.Fatalf("QueueUnenriched: %v", err)
	}
	if queuedCount != 1 || remaining != 1 {
		t.Fatalf("queued %d, remaining %d; want 1 and 1 (oldest first)", queuedCount, remaining)
	}

	// Oldest saved first: the backlog drains in the order it was built.
	got, err := s.GetItem(ctx, older.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.State != model.StateQueued || got.Priority != -1 {
		t.Errorf("older = state %q priority %d, want queued at -1", got.State, got.Priority)
	}

	if _, _, err := s.QueueUnenriched(ctx, -1, 0); err != nil {
		t.Fatalf("QueueUnenriched (all): %v", err)
	}

	for _, id := range []string{imported.ID, older.ID} {
		item, err := s.GetItem(ctx, id)
		if err != nil {
			t.Fatalf("GetItem(%s): %v", id, err)
		}
		if item.State != model.StateQueued {
			t.Errorf("%s = %q, want queued", id, item.State)
		}
	}
	// Untouched: the enriched row stays ready with its summary, the already
	// queued row is not re-stamped, and the bin stays in the bin.
	if item, err := s.GetItem(ctx, read.ID); err != nil || item.State != model.StateReady || item.Summary != "done" {
		t.Errorf("read = %+v, %v; want it ready and unchanged", item, err)
	}
	if item, err := s.GetItem(ctx, queued.ID); err != nil || item.State != model.StateQueued {
		t.Errorf("queued = %+v, %v; want it still queued", item, err)
	}
	if item, err := s.GetItem(ctx, deleted.ID); err != nil || item.State != model.StateReady || item.DeletedAt == nil {
		t.Errorf("deleted = %+v, %v; want it ready and still in the bin", item, err)
	}

	// Nothing is left for a second run to pick up.
	if n, err := s.UnenrichedCount(ctx); err != nil || n != 0 {
		t.Errorf("UnenrichedCount after queueing = %d, %v; want 0", n, err)
	}
}

// The video metadata pass files a video the fetcher could not read. It must not
// be able to overwrite what the owner typed, touch tags no model produced, or
// claim a prompt version that never ran.
func TestApplyVideoMetadata(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// A details_json that came from somewhere else: the metadata pass merges into
	// it rather than replacing what an earlier read found.
	imported := mustCreate(t, s, ItemInput{
		ID: "imported", URL: ptr("https://www.youtube.com/watch?v=dQw4w9WgXcQ"),
		Title:  "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Source: model.SourceImport, Tags: []string{"rain drop tag"},
		DetailsJSON: `{"duration":"PT3M33S","embedUrl":"https://www.youtube.com/embed/dQw4w9WgXcQ"}`,
	})
	typed := mustCreate(t, s, ItemInput{
		ID: "typed", URL: ptr("https://www.youtube.com/watch?v=aaaaaaaaaaa"),
		Title: "My own name for this", Kind: model.KindVideo, KindSource: model.KindSourceManual,
	})

	meta := VideoMetadata{
		Title:         "Never Gonna Give You Up",
		Channel:       "Rick Astley",
		Thumbnail:     "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg",
		PageURL:       "https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		Reason:        "the page could not be read (blocked); filed from the video's public metadata",
		PromptVersion: "oembed.v1",
	}
	updated, err := s.ApplyVideoMetadata(ctx, imported.ID, meta)
	if err != nil {
		t.Fatalf("ApplyVideoMetadata: %v", err)
	}
	// The title was the URL, which is a placeholder: the video's own replaces it.
	if updated.Title != "Never Gonna Give You Up" {
		t.Errorf("title = %q, want the video's", updated.Title)
	}
	if updated.Kind != model.KindVideo || updated.KindSource != model.KindSourceDefault {
		t.Errorf("kind = %q/%q, want video/default", updated.Kind, updated.KindSource)
	}
	if updated.State != model.StateReady || updated.EnrichedAt == nil {
		t.Errorf("state = %q enriched=%v, want ready and stamped", updated.State, updated.EnrichedAt)
	}
	if updated.PromptVersion != "oembed.v1" {
		t.Errorf("prompt_version = %q, want the fallback's marker", updated.PromptVersion)
	}
	if !strings.Contains(updated.ExtractError, "could not be read") {
		t.Errorf("extract_error = %q, want the reason kept", updated.ExtractError)
	}
	// The importer's own tags are untouched: no model ran, so none were earned
	// and none may be dropped.
	tags, err := s.TagsForItem(ctx, imported.ID)
	if err != nil {
		t.Fatalf("TagsForItem: %v", err)
	}
	if len(tags) != 1 || tags[0] != "rain drop tag" {
		t.Errorf("tags = %v, want the imported one alone", tags)
	}

	// A typed title and a manually chosen kind survive.
	kept, err := s.ApplyVideoMetadata(ctx, typed.ID, meta)
	if err != nil {
		t.Fatalf("ApplyVideoMetadata (typed): %v", err)
	}
	if kept.Title != "My own name for this" {
		t.Errorf("title = %q, want the owner's", kept.Title)
	}
	if kept.KindSource != model.KindSourceManual {
		t.Errorf("kind source = %q, want manual kept", kept.KindSource)
	}

	// details_json carries what the panel renders, and merges rather than
	// replaces: the duration an earlier pass stored is still there.
	if !strings.Contains(kept.DetailsJSON, `"author":["Rick Astley"]`) ||
		!strings.Contains(kept.DetailsJSON, "hqdefault.jpg") {
		t.Errorf("details = %s, want the channel and the still", kept.DetailsJSON)
	}
	if !strings.Contains(updated.DetailsJSON, `"duration":"PT3M33S"`) {
		t.Errorf("details = %s, want the existing duration kept", updated.DetailsJSON)
	}
	if !strings.Contains(updated.DetailsJSON, `"embedUrl"`) {
		t.Errorf("details = %s, want the existing embed URL kept", updated.DetailsJSON)
	}

	// The title it just gained is in the index, so the item is findable by it
	// (search's UI is phase 10; the row is phase 2's).
	var found int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM items_fts WHERE items_fts MATCH ?`, `"Never"`).Scan(&found); err != nil {
		t.Fatalf("querying the index: %v", err)
	}
	if found != 1 {
		t.Errorf("FTS found %d rows for the new title, want 1", found)
	}
}

// A pass with no title is refused: the only reason to run it is the title.
func TestApplyVideoMetadataRefusesAnEmptyTitle(t *testing.T) {
	s := openTestStore(t)
	item := mustCreate(t, s, ItemInput{ID: "v", URL: ptr("https://youtu.be/dQw4w9WgXcQ")})
	if _, err := s.ApplyVideoMetadata(context.Background(), item.ID, VideoMetadata{Title: "  "}); err == nil {
		t.Fatal("an empty title was accepted")
	}
}
