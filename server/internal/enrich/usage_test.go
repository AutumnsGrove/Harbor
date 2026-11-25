package enrich

import (
	"context"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// pi-review on #64: deleting the WithUsageItem line in enrich left the whole Go
// suite green, and every usage row would silently lose its item id — which is
// what avgCostPerItem and itemsWithUsage are computed from.

// usageItemDecider records the item id the pipeline attached to its context.
type usageItemDecider struct {
	seen []string
}

func (d *usageItemDecider) Decide(ctx context.Context, _ ClassifyInput) (Classification, error) {
	d.seen = append(d.seen, UsageItem(ctx))
	return Classification{Kind: model.KindArticle}, nil
}

func TestProcessAttachesTheItemToTheUsageContext(t *testing.T) {
	s := openStore(t)
	item := createItem(t, s, store.ItemInput{
		ID: "a", URL: ptr("https://kitchen.example/pancakes"), Title: "Pancakes",
	})

	f, _, w, em := happyFakes()
	decider := &usageItemDecider{}
	en := New(f, decider, w, em, s, nil)
	en.PromptVersion = DefaultPromptVersion
	en.EmbeddingModel = "perplexity/pplx-embed-v1-0.6b"

	if err := en.Process(context.Background(), item); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if len(decider.seen) != 1 {
		t.Fatalf("decider saw %d calls, want 1", len(decider.seen))
	}
	if decider.seen[0] != item.ID {
		t.Errorf("UsageItem(ctx) = %q inside the pipeline, want %q", decider.seen[0], item.ID)
	}
}

// Outside a pipeline there is no item, and that must be an empty string rather
// than a panic or a stale value.
func TestUsageItemIsEmptyWithoutThePipeline(t *testing.T) {
	if got := UsageItem(context.Background()); got != "" {
		t.Errorf("UsageItem(background) = %q, want empty", got)
	}
}
