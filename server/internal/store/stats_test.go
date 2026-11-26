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

// The boundary is the test that matters: saved_at is text in tsLayout, so a
// bound bound the driver's own way ("2006-01-02 15:04:05 +0000 UTC") compares
// wrong at position 10 and leaks a whole calendar day into the window
// (pi-review on #62).
func TestCountsWindowIsExactAtTheBoundary(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// A fixed midday instant: with a `since` taken from the clock, a run between
	// 00:00 and 02:00 UTC puts `outside` on the previous calendar date, where the
	// old binding happens to be right — the test would pass with the bug present
	// (pi-review on #62).
	since := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

	outside := since.Add(-2 * time.Hour)
	inside := since.Add(time.Hour)

	mustCreate(t, s, ItemInput{ID: "outside", Note: "two hours too old", SavedAt: outside})
	mustCreate(t, s, ItemInput{ID: "inside", Note: "an hour inside", SavedAt: inside})

	counts, err := s.Counts(ctx, since)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.SavedThisWeek != 1 {
		t.Errorf("SavedThisWeek = %d, want 1: %v is outside the window and %v is inside",
			counts.SavedThisWeek, outside, inside)
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

// The stored layout is chronological as text; RFC3339 is not. This is why Stamp is
// exported for callers that bind a time into a comparison — the window's edge
// depends on it (review on #94).
func TestStampSortsChronologically(t *testing.T) {
	early := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	late := early.Add(500 * time.Millisecond)

	if !(Stamp(late) > Stamp(early)) {
		t.Errorf("Stamp does not sort: %q <= %q", Stamp(late), Stamp(early))
	}
	// The trap, asserted so nobody "simplifies" the layout back: an RFC3339 bound
	// sorts above a row that is later than it.
	if !(early.Format(time.RFC3339) > Stamp(late)) {
		t.Fatal("RFC3339 no longer mis-sorts here; the reason Stamp is exported has changed")
	}
}
