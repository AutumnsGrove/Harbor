package store

import (
	"context"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// issue #76, design §6: a wall is not a failed save. The row is filed ready from
// what the save already carried, badged by the marker, and it is not in the
// attention list — which is the whole point, because that strip said "couldn't
// be read" about 40 saves that were simply refused.
func TestApplyUnreadableFilesAWallAsReady(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	url := "https://www.nytimes.com/2026/11/01/the-thing.html"
	mustCreate(t, s, ItemInput{ID: "walled", URL: &url, Title: url, Note: "read this on the train"})

	item, err := s.ApplyUnreadable(ctx, "walled", Unreadable{
		Title:  "The Thing, Explained",
		Reason: "the site refused this client (403); filed from its title",
		Marker: "wall.v1",
	})
	if err != nil {
		t.Fatalf("ApplyUnreadable: %v", err)
	}

	if item.State != model.StateReady {
		t.Errorf("state = %q, want ready", item.State)
	}
	if item.PromptVersion != "wall.v1" {
		t.Errorf("prompt_version = %q, want wall.v1 (the marker)", item.PromptVersion)
	}
	if !strings.Contains(item.ExtractError, "403") {
		t.Errorf("extract_error = %q, want the reason it has no text", item.ExtractError)
	}
	if item.Title != "The Thing, Explained" {
		t.Errorf("title = %q, want the page's title over the placeholder", item.Title)
	}
	if item.Note != "read this on the train" {
		t.Errorf("note = %q, want the owner's words untouched", item.Note)
	}
	if item.LinkStatus != "ok" {
		t.Errorf("link_status = %q, want ok: a wall is not a dead link", item.LinkStatus)
	}
	if item.EnrichedAt == nil {
		t.Error("enriched_at is nil; the stage closed, so it should be stamped")
	}
	if item.Attempts != 0 || item.NextAttemptAt != nil || item.LastError != "" {
		t.Errorf("retry columns = %d/%v/%q, want 0/nil/empty", item.Attempts, item.NextAttemptAt, item.LastError)
	}

	failed, err := s.FailedItems(ctx, 50)
	if err != nil {
		t.Fatalf("FailedItems: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("FailedItems = %v, want a walled save out of Needs attention", ids(failed))
	}

	// The new title is what the library is searched by, so FTS must have it.
	if hits := ftsHits(t, s, "explained"); len(hits) != 1 || hits[0] != "walled" {
		t.Errorf("search after filing = %v, want the walled item by its title", hits)
	}
}

// The same path, one fact different: a 404 is a dead link, and the column phase
// 11 owns is where that belongs.
func TestApplyUnreadableMarksADeadLink(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	url := "https://example.com/gone"
	mustCreate(t, s, ItemInput{ID: "dead", URL: &url, Title: "A page that was here"})

	item, err := s.ApplyUnreadable(ctx, "dead", Unreadable{
		Reason: "answered 404 (the page is gone); filed from its title",
		Marker: "dead.v1",
		Dead:   true,
	})
	if err != nil {
		t.Fatalf("ApplyUnreadable: %v", err)
	}
	if item.State != model.StateReady {
		t.Errorf("state = %q, want ready", item.State)
	}
	if item.LinkStatus != "dead" {
		t.Errorf("link_status = %q, want dead", item.LinkStatus)
	}
	if item.LastCheckedAt == nil {
		t.Error("last_checked_at is nil, want the moment we found out")
	}
	if item.Title != "A page that was here" {
		t.Errorf("title = %q, want the saved title kept", item.Title)
	}
}

// Retry is how a lifted wall is found out about (design §6): a walled or dead
// ready row goes back in the queue with the marker, the reason and the verdict
// cleared, so a successful pass leaves no trace of the refusal. A row that is
// merely ready is still not re-enriched.
func TestRetryItemRequeuesAWalledOrDeadRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	url := "https://example.com/walled"
	mustCreate(t, s, ItemInput{ID: "walled", URL: &url, Title: "Walled"})
	if _, err := s.ApplyUnreadable(ctx, "walled", Unreadable{Reason: "403", Marker: "wall.v1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryItem(ctx, "walled"); err != nil {
		t.Fatalf("RetryItem(walled): %v", err)
	}
	item, err := s.GetItem(ctx, "walled")
	if err != nil {
		t.Fatal(err)
	}
	if item.State != model.StateQueued || item.PromptVersion != "" || item.ExtractError != "" {
		t.Errorf("after retry = %q/%q/%q, want queued with a clean slate", item.State, item.PromptVersion, item.ExtractError)
	}

	url2 := "https://example.com/dead"
	mustCreate(t, s, ItemInput{ID: "dead", URL: &url2, Title: "Dead"})
	if _, err := s.ApplyUnreadable(ctx, "dead", Unreadable{Reason: "404", Marker: "dead.v1", Dead: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.RetryItem(ctx, "dead"); err != nil {
		t.Fatalf("RetryItem(dead): %v", err)
	}
	item, err = s.GetItem(ctx, "dead")
	if err != nil {
		t.Fatal(err)
	}
	if item.State != model.StateQueued || item.LinkStatus != "ok" || item.LastCheckedAt != nil {
		t.Errorf("after retry = %q/%q/%v, want queued with link_status ok and no check time",
			item.State, item.LinkStatus, item.LastCheckedAt)
	}

	url3 := "https://example.com/fine"
	mustCreate(t, s, ItemInput{ID: "fine", URL: &url3, Title: "Fine"})
	exec(t, s, `UPDATE items SET state = 'ready', prompt_version = 'tag.v1' WHERE id = 'fine'`)
	if err := s.RetryItem(ctx, "fine"); err != nil {
		t.Fatalf("RetryItem(fine): %v", err)
	}
	item, err = s.GetItem(ctx, "fine")
	if err != nil {
		t.Fatal(err)
	}
	if item.State == model.StateQueued {
		t.Error("a ready item with no complaint was re-queued; retry must not re-enrich a success")
	}
}

// A row with nothing to call it: the URL stays as the title rather than the
// empty string, and the reason still explains it.
func TestApplyUnreadableKeepsTheSavedTitleWhenThereIsNothingBetter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	url := "https://example.com/quiet"
	mustCreate(t, s, ItemInput{ID: "quiet", URL: &url, Title: url})

	item, err := s.ApplyUnreadable(ctx, "quiet", Unreadable{Reason: "403", Marker: "wall.v1"})
	if err != nil {
		t.Fatalf("ApplyUnreadable: %v", err)
	}
	if item.Title != url {
		t.Errorf("title = %q, want the URL left alone", item.Title)
	}
	if item.ExtractError == "" {
		t.Error("extract_error is empty, want the reason it is thin")
	}
}
