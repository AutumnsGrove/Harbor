package store

import (
	"context"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// plans/02 test 8 — the job row and the item commit together. A crash between
// the two is the one failure that either loses a save or duplicates one, so the
// pair is proven here at the store level and again through the queue.

func TestIngestJobWritesTheItemAndTheJobInOneTransaction(t *testing.T) {
	s := openTestStore(t)
	res, err := s.IngestJob(context.Background(), "job-1", ItemInput{
		ID:          "item-1",
		URL:         ptr("https://example.com/a"),
		URLOriginal: "https://example.com/a",
		Title:       "A",
		Note:        "first note",
		Source:      model.SourceTelegram,
	})
	if err != nil {
		t.Fatalf("IngestJob: %v", err)
	}
	if res.Already || res.Merged {
		t.Errorf("first ingest reported already=%v merged=%v, want false/false", res.Already, res.Merged)
	}
	if res.Item.ID != "item-1" {
		t.Errorf("item id = %q, want item-1", res.Item.ID)
	}
	if got := count(t, s.db, `SELECT count(*) FROM processed_jobs WHERE job_id = 'job-1' AND item_id = 'item-1'`); got != 1 {
		t.Errorf("processed_jobs row = %d, want 1", got)
	}
}

// The lease can expire while `done` is in flight, so the same job is claimed
// twice. The second ingest must not append the note again or move saved_at.
func TestIngestJobIsIdempotentByJobID(t *testing.T) {
	s := openTestStore(t)
	in := ItemInput{
		ID:     "item-1",
		URL:    ptr("https://example.com/a"),
		Note:   "only once",
		Source: model.SourceTelegram,
	}
	first, err := s.IngestJob(context.Background(), "job-1", in)
	if err != nil {
		t.Fatalf("first IngestJob: %v", err)
	}

	second, err := s.IngestJob(context.Background(), "job-1", in)
	if err != nil {
		t.Fatalf("second IngestJob: %v", err)
	}
	if !second.Already {
		t.Error("second ingest reported already=false")
	}
	if second.Item.ID != first.Item.ID {
		t.Errorf("second ingest returned item %q, want %q", second.Item.ID, first.Item.ID)
	}
	if n := strings.Count(second.Item.Note, "only once"); n != 1 {
		t.Errorf("note contains the save %d times, want 1", n)
	}
	if !second.Item.SavedAt.Equal(first.Item.SavedAt) {
		t.Errorf("saved_at moved on a replayed job: %v -> %v", first.Item.SavedAt, second.Item.SavedAt)
	}
	if got := count(t, s.db, `SELECT count(*) FROM items`); got != 1 {
		t.Errorf("%d items, want 1", got)
	}
}

// A re-save of the same canonical URL is a merge, and both job ids are recorded
// — replaying either one later is still a no-op (design.md §5).
func TestIngestJobMergesTwoJobsOnTheSameURL(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first, err := s.IngestJob(ctx, "job-1", ItemInput{
		ID:     "item-1",
		URL:    ptr("https://example.com/a"),
		Title:  "first",
		Note:   "one",
		Source: model.SourceShortcut,
	})
	if err != nil {
		t.Fatalf("first IngestJob: %v", err)
	}

	second, err := s.IngestJob(ctx, "job-2", ItemInput{
		ID:     "item-2",
		URL:    ptr("https://example.com/a"),
		Title:  "second",
		Note:   "two",
		Source: model.SourceShortcut,
	})
	if err != nil {
		t.Fatalf("second IngestJob: %v", err)
	}
	if !second.Merged {
		t.Error("second ingest reported merged=false")
	}
	if second.Item.ID != first.Item.ID {
		t.Errorf("second ingest created item %q, want the first item %q", second.Item.ID, first.Item.ID)
	}
	if !strings.Contains(second.Item.Note, "one") || !strings.Contains(second.Item.Note, "two") {
		t.Errorf("merged note lost a save: %q", second.Item.Note)
	}
	if got := count(t, s.db, `SELECT count(*) FROM processed_jobs`); got != 2 {
		t.Errorf("%d processed_jobs rows, want 2", got)
	}
}

// A note job has no URL key, so two note jobs are two items even when the text
// is identical.
func TestIngestJobInsertsEachNoteJob(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	in := ItemInput{ID: "item-1", Note: "same thought", Source: model.SourceTelegram}
	if _, err := s.IngestJob(ctx, "job-1", in); err != nil {
		t.Fatalf("first IngestJob: %v", err)
	}
	in.ID = "item-2"
	res, err := s.IngestJob(ctx, "job-2", in)
	if err != nil {
		t.Fatalf("second IngestJob: %v", err)
	}
	if res.Merged {
		t.Error("a URL-less note job merged; notes have no key to merge on")
	}
	if res.Item.Kind != model.KindNote {
		t.Errorf("kind = %q, want note", res.Item.Kind)
	}
	if got := count(t, s.db, `SELECT count(*) FROM items`); got != 2 {
		t.Errorf("%d items, want 2", got)
	}
}

// If the item write fails, the job must not be recorded: the next claim has to
// be able to try again.
func TestIngestJobRollsBackTheJobRowWhenTheItemFails(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "existing", URL: ptr("https://example.com/dup")})

	// A duplicate URL with an id that merges is fine, so break the item input
	// instead: an invalid kind is rejected by validate before any write.
	_, err := s.IngestJob(ctx, "job-1", ItemInput{
		ID:   "item-1",
		URL:  ptr("https://example.com/a"),
		Kind: model.Kind("nonsense"),
	})
	if err == nil {
		t.Fatal("IngestJob accepted an invalid kind")
	}
	if got := count(t, s.db, `SELECT count(*) FROM processed_jobs`); got != 0 {
		t.Errorf("%d processed_jobs rows after a failed ingest, want 0", got)
	}
	if got := count(t, s.db, `SELECT count(*) FROM items WHERE id = 'item-1'`); got != 0 {
		t.Errorf("the failed ingest left its item behind")
	}
}

func TestIngestJobRejectsAnEmptyID(t *testing.T) {
	s := openTestStore(t)
	_, err := s.IngestJob(context.Background(), "  ", ItemInput{ID: "item-1", Note: "x"})
	if err == nil {
		t.Fatal("IngestJob accepted a blank job id")
	}
}
