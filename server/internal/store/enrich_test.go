package store

import (
	"context"
	"testing"
	"time"
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
		stamp(base.Add(24*time.Hour))); err != nil {
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
