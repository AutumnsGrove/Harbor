package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// mediaFixture is one attachment of an item, with the fields the pipeline sets.
func mediaFixture(t *testing.T, itemID, sha string) model.Media {
	t.Helper()
	w, h := 1200, 1600
	return model.Media{
		ItemID:      itemID,
		Kind:        model.MediaImage,
		Filename:    "IMG_1861.JPG",
		ContentType: "image/jpeg",
		Bytes:       4_400_000,
		SHA256:      sha,
		Width:       &w,
		Height:      &h,
		LocalPath:   sha[:2] + "/" + sha,
	}
}

const (
	shaA = "aa00000000000000000000000000000000000000000000000000000000000001"
	shaB = "bb00000000000000000000000000000000000000000000000000000000000002"
)

// The same bytes attached to the same item twice is one row: that is what makes
// "the same photo saved twice is one attachment" true without the caller looking
// first, and it is the row half of the dedupe the vision call relies on.
func TestAddingTheSameBlobTwiceIsOneRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-1"})

	first, err := s.AddMedia(ctx, mediaFixture(t, item.ID, shaA))
	if err != nil {
		t.Fatalf("AddMedia: %v", err)
	}
	if first.ID == "" {
		t.Fatal("AddMedia returned no id")
	}
	second, err := s.AddMedia(ctx, mediaFixture(t, item.ID, shaA))
	if err != nil {
		t.Fatalf("AddMedia(again): %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("a second attach made a second row: %s and %s", first.ID, second.ID)
	}
	rows, err := s.MediaByItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("%d rows, want 1", len(rows))
	}
}

// Two items sharing one blob are two rows on purpose: each has its own filename,
// its own place in the gallery, and its own mirror key. Reference counting reads
// them together.
func TestOneBlobOnTwoItemsIsTwoRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	one := mustCreate(t, s, ItemInput{ID: "item-2"})
	two := mustCreate(t, s, ItemInput{ID: "item-3"})

	if _, err := s.AddMedia(ctx, mediaFixture(t, one.ID, shaA)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddMedia(ctx, mediaFixture(t, two.ID, shaA)); err != nil {
		t.Fatal(err)
	}
	shared, err := s.MediaBySHA(ctx, shaA)
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 2 {
		t.Errorf("%d rows share the blob, want 2", len(shared))
	}
	// And that count is what a delete consults before unlinking the file.
	if shared[0].LocalPath != shared[1].LocalPath || shared[0].LocalPath != shaA[:2]+"/"+shaA {
		t.Errorf("the two rows disagree about the path: %q and %q", shared[0].LocalPath, shared[1].LocalPath)
	}
}

// A Telegram album is one item with several attachments, in the order they were
// sent: the gallery depends on that order being stable.
func TestAttachmentsComeBackInTheOrderTheyWereAdded(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-4"})
	for _, sha := range []string{shaB, shaA} {
		if _, err := s.AddMedia(ctx, mediaFixture(t, item.ID, sha)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.MediaByItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d rows, want 2", len(rows))
	}
	if rows[0].SHA256 != shaB || rows[1].SHA256 != shaA {
		t.Errorf("order = %s, %s; want the order they were added", rows[0].SHA256[:4], rows[1].SHA256[:4])
	}
	// The nullable columns come back as pointers, not zeroes.
	if rows[0].Width == nil || *rows[0].Width != 1200 || rows[0].Height == nil || *rows[0].Height != 1600 {
		t.Errorf("dimensions = %v x %v, want 1200x1600", rows[0].Width, rows[0].Height)
	}
	if rows[0].CreatedAt.IsZero() {
		t.Error("created_at did not come back")
	}
}

// The mirror queue is "every row with no key yet", oldest first, so a backlog
// drains in the order it arrived.
func TestMediaNeedingMirrorIsTheQueue(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-5"})
	for _, sha := range []string{shaA, shaB} {
		if _, err := s.AddMedia(ctx, mediaFixture(t, item.ID, sha)); err != nil {
			t.Fatal(err)
		}
	}
	queue, err := s.MediaNeedingMirror(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 2 {
		t.Fatalf("%d rows queued, want 2", len(queue))
	}
	// Mirror the first: it leaves the queue and the other stays.
	if err := s.SetMediaMirror(ctx, queue[0].ID, "media/"+queue[0].SHA256[:2]+"/"+queue[0].SHA256); err != nil {
		t.Fatalf("SetMediaMirror: %v", err)
	}
	queue, err = s.MediaNeedingMirror(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) != 1 {
		t.Fatalf("%d rows queued after one mirror, want 1", len(queue))
	}
	if queue[0].SHA256 != shaB {
		t.Errorf("the row left in the queue is %s, want the unm mirrored one", queue[0].SHA256[:4])
	}
	// A mirrored row reports its key, which is what a later integrity check reads.
	rows, err := s.MediaByItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.SHA256 == shaA {
			if row.R2Key != "media/"+shaA[:2]+"/"+shaA {
				t.Errorf("r2_key = %q", row.R2Key)
			}
		}
	}
	// The limit is honoured, so a big backlog drains in batches.
	if _, err := s.MediaNeedingMirror(ctx, 1); err != nil {
		t.Fatalf("MediaNeedingMirror(1): %v", err)
	}
}

// An empty key is not a mirror: accepting it would make a failed upload look
// successful and the row would be retried forever without anyone noticing.
func TestAnEmptyMirrorKeyIsRefused(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-6"})
	row, err := s.AddMedia(ctx, mediaFixture(t, item.ID, shaA))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMediaMirror(ctx, row.ID, ""); err == nil {
		t.Fatal("an empty r2_key was accepted")
	}
	if err := s.SetMediaMirror(ctx, "no-such-row", "media/aa/"+shaA); err == nil {
		t.Fatal("a mirror was recorded against a row that does not exist")
	}
}

// A sha256 is a path and an R2 key, so a row cannot carry something that is not
// one — and an unknown kind cannot be stored either.
func TestAddMediaValidatesItsInput(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-7"})

	cases := map[string]model.Media{
		"no item":       {Kind: model.MediaImage, SHA256: shaA, LocalPath: "aa/x"},
		"bad kind":      {ItemID: item.ID, Kind: "screenshot", SHA256: shaA, LocalPath: "aa/x"},
		"short hash":    {ItemID: item.ID, Kind: model.MediaImage, SHA256: "abc", LocalPath: "aa/x"},
		"no local path": {ItemID: item.ID, Kind: model.MediaImage, SHA256: shaA},
	}
	for name, m := range cases {
		if _, err := s.AddMedia(ctx, m); err == nil {
			t.Errorf("%s: AddMedia accepted it", name)
		}
	}
	if _, err := s.MediaBySHA(ctx, "not-a-hash"); err == nil {
		t.Error("MediaBySHA accepted a value that is not a hash")
	}
}

// Dimensions are recorded once the thumbnail has decoded the image, and a
// negative size is not a size.
func TestSetMediaDimensions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-8"})
	row, err := s.AddMedia(ctx, mediaFixture(t, item.ID, shaA))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMediaDimensions(ctx, row.ID, 1200, 1600); err != nil {
		t.Fatalf("SetMediaDimensions: %v", err)
	}
	rows, err := s.MediaByItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if *rows[0].Width != 1200 || *rows[0].Height != 1600 {
		t.Errorf("dims = %d x %d", *rows[0].Width, *rows[0].Height)
	}
	if err := s.SetMediaDimensions(ctx, row.ID, -1, 10); err == nil {
		t.Error("a negative width was accepted")
	}
	// An unknown id is an error rather than a silent no-op, the same as
	// SetMediaMirror: a purge can remove the row while the pipeline holds it
	// (found in review).
	if err := s.SetMediaDimensions(ctx, "no-such-row", 10, 10); err == nil {
		t.Error("dimensions were recorded against a row that does not exist")
	}
}

// Deleting an item takes its attachments with it (the FK cascade), which is what
// stops a purged item leaving rows pointing at nothing.
func TestDeletingAnItemDeletesItsMedia(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-9"})
	if _, err := s.AddMedia(ctx, mediaFixture(t, item.ID, shaA)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, item.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.MediaBySHA(ctx, shaA)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("%d media rows survived their item", len(rows))
	}
}

// The store hands back the kind it was given, for every kind in the enum.
func TestEveryMediaKindRoundTrips(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-10"})
	for i, kind := range model.MediaKinds() {
		if !kind.Valid() {
			t.Fatalf("%q is in MediaKinds but not Valid", kind)
		}
		sha := strings.Repeat("c", 62) + "0" + string(rune('0'+i))
		m := mediaFixture(t, item.ID, sha)
		m.Kind = kind
		if _, err := s.AddMedia(ctx, m); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
	rows, err := s.MediaByItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(model.MediaKinds()) {
		t.Errorf("%d rows for %d kinds", len(rows), len(model.MediaKinds()))
	}
}

// The queue has a default batch size, so a caller that passes zero (or nothing)
// still drains instead of asking for an empty page forever.
func TestMediaNeedingMirrorDefaultsItsBatchSize(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-default-batch"})
	if _, err := s.AddMedia(ctx, mediaFixture(t, item.ID, shaA)); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, -5} {
		queue, err := s.MediaNeedingMirror(ctx, limit)
		if err != nil {
			t.Fatalf("MediaNeedingMirror(%d): %v", limit, err)
		}
		if len(queue) != 1 {
			t.Errorf("MediaNeedingMirror(%d) returned %d rows, want the default batch", limit, len(queue))
		}
	}
}

// derived_at is written by the thumbnail task and read by the re-derive sweep, so
// it has to survive the round trip: it is a nullable timestamp, which is the
// column shape most likely to come back wrong.
func TestDerivedAtSurvivesTheRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	item := mustCreate(t, s, ItemInput{ID: "item-derived"})
	m := mediaFixture(t, item.ID, shaB)
	at := time.Date(2026, 11, 27, 13, 45, 0, 0, time.UTC)
	m.DerivedAt = &at

	if _, err := s.AddMedia(ctx, m); err != nil {
		t.Fatal(err)
	}
	rows, err := s.MediaByItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d rows, want 1", len(rows))
	}
	if rows[0].DerivedAt == nil {
		t.Fatal("derived_at came back nil after being set")
	}
	if !rows[0].DerivedAt.Equal(at) {
		t.Errorf("derived_at = %v, want %v", *rows[0].DerivedAt, at)
	}
	// And a row that was never derived has no timestamp, rather than the epoch.
	plain, err := s.AddMedia(ctx, mediaFixture(t, item.ID, shaA))
	if err != nil {
		t.Fatal(err)
	}
	if plain.DerivedAt != nil {
		t.Errorf("a fresh row has derived_at %v", *plain.DerivedAt)
	}
}
