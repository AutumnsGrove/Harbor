package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
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

	enricher := NewEnricher(s, nil, nil)
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

// recordingProcessor stands in for enrich.Enricher and closes the item, so the
// pass has a finite queue to drain.
type recordingProcessor struct {
	store *store.Store
	calls []string
	errs  map[string]error
}

func (p *recordingProcessor) Process(ctx context.Context, item model.Item) error {
	p.calls = append(p.calls, item.ID)
	if err := p.errs[item.ID]; err != nil {
		// Mirror the pipeline: record the terminal failure on the row.
		_, _ = p.store.RecordEnrichmentFailure(ctx, item.ID, err.Error(), nil)
		return err
	}
	return p.store.MarkReady(ctx, item.ID)
}

func TestEnricherRunsTheProcessorAndKeepsGoingAfterAFailure(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.CreateItem(ctx, storeInput("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateItem(ctx, storeInput("b")); err != nil {
		t.Fatal(err)
	}
	processor := &recordingProcessor{store: s, errs: map[string]error{"a": errors.New("boom")}}
	enricher := NewEnricher(s, processor, nil)

	n, err := enricher.Pass(ctx)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if n != 2 || len(processor.calls) != 2 {
		t.Fatalf("Pass = %d, calls = %v; want both items attempted", n, processor.calls)
	}
	if enricher.failed != 1 {
		t.Errorf("failed = %d, want 1", enricher.failed)
	}
	a, _ := s.GetItem(ctx, "a")
	if a.State != model.StateFailed {
		t.Errorf("item a state = %q, want failed", a.State)
	}
	b, _ := s.GetItem(ctx, "b")
	if b.State != model.StateReady {
		t.Errorf("item b state = %q, want ready", b.State)
	}
	if queued, _ := s.QueuedItems(ctx, 10); len(queued) != 0 {
		t.Errorf("queue still holds %d items", len(queued))
	}
}
