package store

import (
	"context"
	"errors"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// Recently deleted is its own view: deleted rows only, not every row. The
// export path still uses IncludeDeleted, which is why both exist.
func TestListItemsOnlyDeleted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "live", Note: "still here"})
	mustCreate(t, s, ItemInput{ID: "gone", Note: "in the bin"})
	if err := s.SoftDeleteItem(ctx, "gone"); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}

	deleted, err := s.ListItems(ctx, ListOptions{OnlyDeleted: true})
	if err != nil {
		t.Fatalf("ListItems(OnlyDeleted): %v", err)
	}
	if len(deleted) != 1 || deleted[0].ID != "gone" {
		t.Fatalf("OnlyDeleted = %v, want just the deleted item", ids(deleted))
	}

	live, err := s.ListItems(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(live) != 1 || live[0].ID != "live" {
		t.Errorf("default list = %v, want just the live item", ids(live))
	}
}

// The composer's merge warning asks by canonical URL, and a soft-deleted match
// counts because re-saving restores it (design.md §5).
func TestGetItemByURLFindsADeletedRow(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustCreate(t, s, ItemInput{ID: "a", URL: ptr("https://example.com/a"), Note: "x"})

	item, err := s.GetItemByURL(ctx, "https://example.com/a")
	if err != nil {
		t.Fatalf("GetItemByURL: %v", err)
	}
	if item.ID != "a" {
		t.Errorf("id = %q, want a", item.ID)
	}

	if err := s.SoftDeleteItem(ctx, "a"); err != nil {
		t.Fatalf("SoftDeleteItem: %v", err)
	}
	if item, err = s.GetItemByURL(ctx, "https://example.com/a"); err != nil || item.ID != "a" {
		t.Errorf("GetItemByURL after delete = %v, %v; want the deleted row", item.ID, err)
	}

	if _, err := s.GetItemByURL(ctx, "https://example.com/missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetItemByURL on a missing URL = %v, want ErrNotFound", err)
	}
}

func ids(items []model.Item) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.ID
	}
	return out
}
