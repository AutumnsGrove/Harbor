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

	n, _, err := enricher.Pass(ctx)
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
	if n, _, err = enricher.Pass(ctx); err != nil || n != 1 {
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

	n, failed, err := enricher.Pass(ctx)
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if n != 2 || len(processor.calls) != 2 {
		t.Fatalf("Pass = %d, calls = %v; want both items attempted", n, processor.calls)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
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

// The failed count belongs to the pass that produced it. It used to be a field on
// the Enricher that was never reset, so a pass that took 5 rows could report
// "failed:11" — the failures of every pass since the process started (issue #80).
func TestFailedCountBelongsToThePass(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.CreateItem(ctx, storeInput("a")); err != nil {
		t.Fatal(err)
	}
	processor := &recordingProcessor{store: s, errs: map[string]error{"a": errors.New("boom")}}
	enricher := NewEnricher(s, processor, nil)

	if processed, failed, err := enricher.Pass(ctx); err != nil || processed != 1 || failed != 1 {
		t.Fatalf("first pass = %d processed, %d failed, %v; want 1 and 1", processed, failed, err)
	}

	// A second pass with a row that succeeds must not inherit the first pass's
	// failure.
	if _, err := s.CreateItem(ctx, storeInput("b")); err != nil {
		t.Fatal(err)
	}
	processed, failed, err := enricher.Pass(ctx)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if processed != 1 {
		t.Errorf("second pass processed = %d, want 1", processed)
	}
	if failed != 0 {
		t.Errorf("second pass failed = %d, want 0: the count carried over from the first pass", failed)
	}
	if failed > processed {
		t.Errorf("failed (%d) is larger than processed (%d): the two numbers cannot be read together", failed, processed)
	}
}

// A pass stopped by cancellation is not a pass with a failure in it. The item is
// unchanged and the next pass finishes it; counting it would report a failure for
// a row that never failed (issue #80, review on #91).
func TestCancelledPassDoesNotCountTheItemAsFailed(t *testing.T) {
	s := openStore(t)
	if _, err := s.CreateItem(context.Background(), storeInput("a")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	processor := &cancellingProcessor{cancel: cancel, store: s}
	enricher := NewEnricher(s, processor, nil)

	processed, failed, err := enricher.Pass(ctx)
	if err == nil {
		t.Fatal("Pass hid the cancellation")
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0: the run stopped, the item did not fail", failed)
	}
	if processed != 0 {
		t.Errorf("processed = %d, want 0: the item was handed back, not finished", processed)
	}
}

type cancellingProcessor struct {
	cancel context.CancelFunc
	store  *store.Store
}

func (p *cancellingProcessor) Process(ctx context.Context, _ model.Item) error {
	p.cancel()
	return ctx.Err()
}
