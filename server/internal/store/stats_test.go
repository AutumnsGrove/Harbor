package store

import (
	"context"
	"testing"
	"time"
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

	counts, err := s.Counts(ctx, time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	// All three live rows were saved just now, so the weekly window holds them
	// too; the soft-deleted one is in none of the numbers.
	if want := (Counts{Items: 3, Pending: 1, Failed: 1, SavedThisWeek: 3}); counts != want {
		t.Errorf("Counts = %+v, want %+v", counts, want)
	}
}

// saved_this_week is a window, not a total, and it counts only live rows.
func TestCountsWindowExcludesOlderSaves(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	old := time.Now().UTC().AddDate(0, 0, -30)
	mustCreate(t, s, ItemInput{ID: "old", Note: "a month ago", SavedAt: old})
	mustCreate(t, s, ItemInput{ID: "new", Note: "now"})

	counts, err := s.Counts(ctx, time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.Items != 2 {
		t.Errorf("Items = %d, want 2", counts.Items)
	}
	if counts.SavedThisWeek != 1 {
		t.Errorf("SavedThisWeek = %d, want 1: only the save inside the window", counts.SavedThisWeek)
	}
}

func TestCountsOnAnEmptyStore(t *testing.T) {
	counts, err := openTestStore(t).Counts(context.Background(), time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts != (Counts{}) {
		t.Errorf("Counts = %+v, want zeroes", counts)
	}
}
