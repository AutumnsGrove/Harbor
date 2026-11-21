package store

import (
	"context"
	"testing"
)

func TestCountsSeparatesLiveQueuedFailedAndDeleted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "live", Note: "queued by default"})
	mustCreate(t, s, ItemInput{ID: "ready", Note: "done"})
	mustCreate(t, s, ItemInput{ID: "broken", Note: "failed"})
	mustCreate(t, s, ItemInput{ID: "gone", Note: "deleted"})

	if _, err := s.db.ExecContext(ctx, `UPDATE items SET state = 'ready' WHERE id = 'ready'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE items SET state = 'failed' WHERE id = 'broken'`); err != nil {
		t.Fatal(err)
	}
	if err := s.SoftDeleteItem(ctx, "gone"); err != nil {
		t.Fatal(err)
	}

	counts, err := s.Counts(ctx)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if want := (Counts{Items: 3, Pending: 1, Failed: 1}); counts != want {
		t.Errorf("Counts = %+v, want %+v", counts, want)
	}
}

func TestCountsOnAnEmptyStore(t *testing.T) {
	counts, err := openTestStore(t).Counts(context.Background())
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts != (Counts{}) {
		t.Errorf("Counts = %+v, want zeroes", counts)
	}
}
