package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// plans/03 — the attention strip reads failures from the database, and retry
// puts the item back in the queue. The strip must survive a restart, so it
// cannot be assembled from logs.
func TestAttentionListsFailedItemsAndRetryRequeues(t *testing.T) {
	h, library := itemsHandler(t)
	ctx := context.Background()
	failed := saveItem(t, h, map[string]any{"note": "a save that failed"})
	fine := saveItem(t, h, map[string]any{"note": "a save that worked"})
	if _, err := library.RecordEnrichmentFailure(ctx, failed.Item.ID, "page too large", nil); err != nil {
		t.Fatalf("RecordEnrichmentFailure: %v", err)
	}

	rec := itemsRequest(h, http.MethodGet, "/api/attention", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/attention = %d (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, failed.Item.ID) || !strings.Contains(body, "page too large") {
		t.Errorf("attention = %s, want the failed item and its reason", body)
	}
	if strings.Contains(body, fine.Item.ID) {
		t.Error("a healthy item is in the attention list")
	}

	rec = itemsRequest(h, http.MethodPost, "/api/items/"+failed.Item.ID+"/retry", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"queued"`) {
		t.Errorf("retry = %d %s, want 200 and queued", rec.Code, rec.Body.String())
	}

	rec = itemsRequest(h, http.MethodGet, "/api/attention", nil)
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("attention after retry = %s, want empty", rec.Body.String())
	}

	if rec := itemsRequest(h, http.MethodPost, "/api/items/missing/retry", nil); rec.Code != http.StatusNotFound {
		t.Errorf("retry of a missing item = %d, want 404", rec.Code)
	}
}
