package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Issue #52: the panel reads one round trip, so the endpoint test is about the
// shape and the two numbers the panel cannot compute itself — the backlog and
// the budget.

func usageHandler(t *testing.T, budget float64) (http.Handler, *store.Store) {
	t.Helper()
	cfg := testConfig(t)
	cfg.AIBudgetUSD = budget

	library, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = library.Close() })

	h := New(cfg, slog.New(slog.DiscardHandler), testAssets(), WithStore(library)).Handler()
	return h, library
}

func TestUsageServesThePanelInOneRoundTrip(t *testing.T) {
	h, library := usageHandler(t, 5)
	ctx := context.Background()

	if _, err := library.CreateItem(ctx, store.ItemInput{ID: "a", Note: "queued"}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}

	now := time.Now().UTC()
	for _, call := range []store.Usage{
		{At: now, Surface: "classify", RequestedModel: "typesafe/jev-1.14", ServedModel: "typesafe/jev-1.14", PromptTokens: 900, CostUSD: 0.0004, ItemID: "a"},
		{At: now, Surface: "write", RequestedModel: "deepseek/deepseek-v4.1-flash-1017", ServedModel: "deepseek/deepseek-v4.1-flash-1017", PromptTokens: 1800, CompletionTokens: 300, CostUSD: 0.0010, ItemID: "a"},
		{At: now, Surface: "embed", RequestedModel: "perplexity/pplx-embed-v1-0.6b", ServedModel: "perplexity/pplx-embed-v1-0.6b", PromptTokens: 700, CostUSD: 0.0001, ItemID: "a"},
	} {
		if err := library.RecordUsage(ctx, call); err != nil {
			t.Fatalf("RecordUsage: %v", err)
		}
	}

	body := decode(t, request(h, http.MethodGet, "/api/usage", map[string]string{
		identityHeader: "autumn@grove.place",
	}))

	if body["calls"] != float64(3) {
		t.Errorf("calls = %v, want 3", body["calls"])
	}
	if got := body["budgetUsd"]; got != float64(5) {
		t.Errorf("budgetUsd = %v, want 5 from config", got)
	}
	// The item is queued, so the panel can estimate what is left to spend.
	if got := body["backlogItems"]; got != float64(1) {
		t.Errorf("backlogItems = %v, want 1", got)
	}
	if got := body["days"]; got != float64(30) {
		t.Errorf("days = %v, want the 30 default", got)
	}

	surfaces, ok := body["bySurface"].([]any)
	if !ok || len(surfaces) != 3 {
		t.Fatalf("bySurface = %v, want three groups", body["bySurface"])
	}
	// The split has to add up to the total the panel shows beside it.
	var sum float64
	for _, entry := range surfaces {
		group := entry.(map[string]any)
		sum += group["usd"].(float64)
	}
	if diff := sum - body["totalUsd"].(float64); diff > 1e-9 || diff < -1e-9 {
		t.Errorf("bySurface sums to %v, total is %v", sum, body["totalUsd"])
	}

	// Cost per save comes from distinct items, not distinct calls.
	if got := body["avgCostPerItem"].(float64); got <= 0 {
		t.Errorf("avgCostPerItem = %v, want the all-time spend over one item", got)
	}
	if got := body["itemsWithUsage"]; got != float64(1) {
		t.Errorf("itemsWithUsage = %v, want 1", got)
	}
}

// A database with no calls answers zeros and empty series; the panel turns that
// into "no calls recorded yet" rather than a confident $0.00.
func TestUsageOnAFreshLibraryIsEmptyNotWrong(t *testing.T) {
	h, _ := usageHandler(t, 5)

	body := decode(t, request(h, http.MethodGet, "/api/usage", map[string]string{
		identityHeader: "autumn@grove.place",
	}))

	if body["calls"] != float64(0) || body["totalUsd"] != float64(0) {
		t.Errorf("usage = %v, want zeroes", body)
	}
	if series, ok := body["bySurface"].([]any); !ok || len(series) != 0 {
		t.Errorf("bySurface = %v, want an empty array", body["bySurface"])
	}
	if series, ok := body["byDay"].([]any); !ok || len(series) != 0 {
		t.Errorf("byDay = %v, want an empty array", body["byDay"])
	}
}

func TestUsageWindowIsValidated(t *testing.T) {
	h, _ := usageHandler(t, 5)

	for _, days := range []string{"0", "-1", "366", "thirty"} {
		rec := request(h, http.MethodGet, "/api/usage?days="+days, map[string]string{
			identityHeader: "autumn@grove.place",
		})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("days=%s answered %d, want 400", days, rec.Code)
		}
	}
}

func TestUsageIsBehindTheIdentityLock(t *testing.T) {
	h, _ := usageHandler(t, 5)
	rec := request(h, http.MethodGet, "/api/usage", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/usage with no identity = %d, want 403", rec.Code)
	}
}
