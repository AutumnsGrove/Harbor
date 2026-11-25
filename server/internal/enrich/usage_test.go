package enrich

import (
	"context"
	"net/url"
	"strings"
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

// Issue #63: og:image is allowed to be relative, and the extractor resolves it
// against the URL it actually fetched — after redirects. The pipeline stored the
// raw value, so a later reader resolved it against the saved URL, and a
// shortened link's cover never fetched.
func TestEnrichmentStoresTheCoverResolvedAgainstTheFinalURL(t *testing.T) {
	s := openStore(t)
	item := createItem(t, s, store.ItemInput{
		ID: "a", URL: ptr("https://short.example/abc"), Title: "A post",
	})

	final, err := url.Parse("https://blog.example/posts/one")
	if err != nil {
		t.Fatal(err)
	}
	f, d, w, em := happyFakes()
	f.body = `<html><head><title>A post</title>
		<meta property="og:image" content="/images/cover.png">
		<meta property="og:site_name" content="Blog">
		</head><body><article><p>Whisk the batter.</p></article></body></html>`
	f.final = final

	en := newTestEnricher(t, s, f, d, w, em)
	if err := en.Process(context.Background(), item); err != nil {
		t.Fatalf("Process: %v", err)
	}

	stored, err := s.GetItem(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	want := "https://blog.example/images/cover.png"
	if !strings.Contains(stored.OGJSON, want) {
		t.Errorf("og_json = %q, want the cover resolved against the final URL %q", stored.OGJSON, want)
	}
	if strings.Contains(stored.OGJSON, `"/images/cover.png"`) {
		t.Errorf("og_json = %q still holds the raw relative image", stored.OGJSON)
	}
}
