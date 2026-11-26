package store

import (
	"context"
	"testing"
	"time"
)

// Issue #67: the pipeline now stores the cover resolved against the fetched
// page, which heals new saves. These rows were written before that, and no other
// path can re-process them — QueueUnenriched only takes rows the model has never
// read, and RetryItem only failed ones.

func TestRelativeCoverCountFindsOnlyUnusableImages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	cases := map[string]struct {
		og   string
		want bool
	}{
		"root relative image":     {`{"image":"/images/og.png"}`, true},
		"protocol relative image": {`{"image":"//cdn.example/og.png"}`, true},
		"relative importer cover": {`{"cover":"images/og.png"}`, true},
		"absolute https image":    {`{"image":"https://example.test/og.png"}`, false},
		"absolute http image":     {`{"image":"http://example.test/og.png"}`, false},
		"absolute importer cover": {`{"cover":"https://cdn.example/og.png","excerpt":"x"}`, false},
		"no image at all":         {`{"excerpt":"just words"}`, false},
		"empty image":             {`{"image":""}`, false},
		"empty og object":         {`{}`, false},
		"not JSON at all":         {`{not json`, false},
		"only a title":            {`{"title":"T"}`, false},
	}

	index := 0
	for name, tc := range cases {
		index++
		id := "item-" + string(rune('a'+index))
		if _, err := s.CreateItem(ctx, ItemInput{ID: id, Note: name, OGJSON: tc.og}); err != nil {
			t.Fatalf("%s: CreateItem: %v", name, err)
		}
		// A finished row: the state these rows are actually in.
		if _, err := s.db.ExecContext(ctx, `UPDATE items SET state = 'ready', prompt_version = 'tag.v1', enriched_at = ? WHERE id = ?`, Stamp(time.Now().UTC()), id); err != nil {
			t.Fatalf("%s: marking ready: %v", name, err)
		}

		want := 0
		if tc.want {
			want = 1
		}
		var got int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE id = ? AND `+relativeCoverWhere, id).Scan(&got); err != nil {
			t.Fatalf("%s: counting: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: matched = %d, want %d", name, got, want)
		}
	}
}

func TestQueueRelativeCoversRequiresAFinishedRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// A row still queued is not this command's business, and neither is a
	// soft-deleted one.
	queued := mustCreate(t, s, ItemInput{ID: "queued", Note: "queued", OGJSON: `{"image":"/og.png"}`})
	deleted := mustCreate(t, s, ItemInput{ID: "deleted", Note: "deleted", OGJSON: `{"image":"/og.png"}`})
	if err := s.SoftDeleteItem(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	ready := mustCreate(t, s, ItemInput{ID: "ready", Note: "ready", OGJSON: `{"image":"/og.png"}`})
	if _, err := s.db.ExecContext(ctx, `UPDATE items SET state = 'ready', prompt_version = 'tag.v1' WHERE id = ?`, ready.ID); err != nil {
		t.Fatal(err)
	}

	count, err := s.RelativeCoverCount(ctx)
	if err != nil {
		t.Fatalf("RelativeCoverCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("RelativeCoverCount = %d, want 1 (only the ready row)", count)
	}

	queuedCount, remaining, err := s.QueueRelativeCovers(ctx, -1, 0)
	if err != nil {
		t.Fatalf("QueueRelativeCovers: %v", err)
	}
	if queuedCount != 1 || remaining != 0 {
		t.Errorf("queued = %d, remaining = %d, want 1 and 0", queuedCount, remaining)
	}

	stored, err := s.GetItem(ctx, ready.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "queued" {
		t.Errorf("state = %q, want queued", stored.State)
	}
	// The row is queued where the drain will take it, at the bulk priority.
	if stored.Priority != -1 {
		t.Errorf("priority = %d, want -1", stored.Priority)
	}

	// The other two rows were untouched.
	for _, id := range []string{queued.ID, deleted.ID} {
		row, err := s.GetItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if id == queued.ID && row.State != "queued" {
			t.Errorf("the already-queued row changed to %q", row.State)
		}
	}

	// Idempotent: a second run finds nothing.
	again, remainingAgain, err := s.QueueRelativeCovers(ctx, -1, 0)
	if err != nil {
		t.Fatalf("second QueueRelativeCovers: %v", err)
	}
	if again != 0 || remainingAgain != 0 {
		t.Errorf("second run queued = %d, remaining = %d, want 0 and 0", again, remainingAgain)
	}
}

func TestQueueRelativeCoversHonoursLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for _, id := range []string{"a", "b", "c"} {
		created := mustCreate(t, s, ItemInput{ID: id, Note: id, OGJSON: `{"image":"/og.png"}`})
		if _, err := s.db.ExecContext(ctx, `UPDATE items SET state = 'ready', prompt_version = 'tag.v1' WHERE id = ?`, created.ID); err != nil {
			t.Fatal(err)
		}
	}

	queued, remaining, err := s.QueueRelativeCovers(ctx, -1, 2)
	if err != nil {
		t.Fatalf("QueueRelativeCovers: %v", err)
	}
	if queued != 2 || remaining != 1 {
		t.Errorf("queued = %d, remaining = %d, want 2 and 1", queued, remaining)
	}
}
