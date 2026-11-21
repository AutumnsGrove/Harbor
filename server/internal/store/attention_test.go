package store

import (
	"context"
	"errors"
	"testing"
)

func TestFailedItemsAndRetry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "a", Note: "fails"})
	mustCreate(t, s, ItemInput{ID: "b", Note: "fine"})
	if _, err := s.RecordEnrichmentFailure(ctx, "a", "page too large", nil); err != nil {
		t.Fatalf("RecordEnrichmentFailure: %v", err)
	}

	items, err := s.FailedItems(ctx, 10)
	if err != nil {
		t.Fatalf("FailedItems: %v", err)
	}
	if len(items) != 1 || items[0].ID != "a" || items[0].LastError != "page too large" {
		t.Fatalf("FailedItems = %v, want item a with its reason", ids(items))
	}

	if err := s.RetryItem(ctx, "a"); err != nil {
		t.Fatalf("RetryItem: %v", err)
	}
	item, _ := s.GetItem(ctx, "a")
	if item.State != "queued" || item.Attempts != 0 || item.NextAttemptAt != nil || item.LastError != "" {
		t.Errorf("after retry = state %q, attempts %d, next %v, error %q", item.State, item.Attempts, item.NextAttemptAt, item.LastError)
	}
	if items, _ := s.FailedItems(ctx, 10); len(items) != 0 {
		t.Errorf("FailedItems still holds %v", ids(items))
	}

	// Idempotent for an item that is already fine, 404 for one that is gone.
	if err := s.RetryItem(ctx, "b"); err != nil {
		t.Errorf("retrying a queued item = %v, want nil", err)
	}
	if err := s.RetryItem(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("retrying a missing item = %v, want ErrNotFound", err)
	}
}
