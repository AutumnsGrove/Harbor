package queue

import (
	"context"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

func storeInput(id string) store.ItemInput {
	return store.ItemInput{ID: id, Note: "note " + id}
}

func listAll() store.ListOptions { return store.ListOptions{} }

// plans/02 — the enrichment shell. Phase 2 has no model to call, so "enriched"
// means the item left the queue in priority order; phase 3 replaces the body
// of the pass, not its shape.
func TestEnricherDrainsTheQueueInBatches(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.CreateItem(ctx, storeInput(id)); err != nil {
			t.Fatalf("CreateItem: %v", err)
		}
	}

	enricher := NewEnricher(s, nil)
	enricher.batch = 2 // force more than one round trip

	n, err := enricher.Pass(ctx)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if n != 3 {
		t.Errorf("Pass marked %d ready, want 3", n)
	}
	items, err := s.ListItems(ctx, listAll())
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	for _, item := range items {
		if item.State != "ready" {
			t.Errorf("item %s state = %q, want ready", item.ID, item.State)
		}
	}
	if queued, _ := s.QueuedItems(ctx, 10); len(queued) != 0 {
		t.Errorf("queue still holds %d items", len(queued))
	}

	// A save made after the pass is picked up by the next one.
	if _, err := s.CreateItem(ctx, storeInput("d")); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if n, err = enricher.Pass(ctx); err != nil || n != 1 {
		t.Errorf("second Pass = %d, %v; want 1", n, err)
	}
	if run, problem := enricher.Status(); run.IsZero() || problem != "" {
		t.Errorf("Status = %v, %q; want a run and no error", run, problem)
	}
}
