package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
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

// issue #76: a wall or a dead link is filed with a badge, not parked, and the
// API is where the card learns which badge to draw. A walled save is in the
// library and out of Needs attention.
func TestAWalledSaveIsBadgedAndNotInNeedsAttention(t *testing.T) {
	h, library := itemsHandler(t)
	ctx := context.Background()
	walled := saveItem(t, h, map[string]any{"url": "https://www.nytimes.com/2026/11/01/thing.html"})
	dead := saveItem(t, h, map[string]any{"url": "https://example.com/gone"})

	if _, err := library.ApplyUnreadable(ctx, walled.Item.ID, store.Unreadable{
		Reason: "the site refuses this client (answered 403)", Marker: model.WallVersion,
	}); err != nil {
		t.Fatalf("ApplyUnreadable(wall): %v", err)
	}
	if _, err := library.ApplyUnreadable(ctx, dead.Item.ID, store.Unreadable{
		Reason: "the page is gone (answered 404)", Marker: model.DeadVersion, Dead: true,
	}); err != nil {
		t.Fatalf("ApplyUnreadable(dead): %v", err)
	}

	rec := itemsRequest(h, http.MethodGet, "/api/items/"+walled.Item.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET item = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"walled":true`) {
		t.Errorf("walled item = %s, want the badge flag", body)
	}
	if !strings.Contains(body, `"state":"ready"`) {
		t.Errorf("walled item = %s, want state ready", body)
	}

	rec = itemsRequest(h, http.MethodGet, "/api/items/"+dead.Item.ID, nil)
	if body := rec.Body.String(); !strings.Contains(body, `"linkStatus":"dead"`) {
		t.Errorf("dead item = %s, want linkStatus dead", body)
	}

	rec = itemsRequest(h, http.MethodGet, "/api/attention", nil)
	if body := rec.Body.String(); !strings.Contains(body, `"items":[]`) {
		t.Errorf("attention = %s, want neither save in it", body)
	}

	// Retry is the path a lifted wall takes, and the API has to allow it on a
	// ready row or there is no way back.
	rec = itemsRequest(h, http.MethodPost, "/api/items/"+walled.Item.ID+"/retry", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"state":"queued"`) {
		t.Errorf("retry of a walled save = %d %s, want 200 and queued", rec.Code, rec.Body.String())
	}
}

// issue #74: a document with no text layer is the third "thin, not failed"
// shape. It shares the badge machinery with a wall, and its own flag so the card
// can say "No text" rather than "Walled".
func TestADocumentWithNoTextIsBadgedAndNotInNeedsAttention(t *testing.T) {
	h, library := itemsHandler(t)
	ctx := context.Background()
	scan := saveItem(t, h, map[string]any{"url": "https://example.com/scanned-report.pdf"})

	if _, err := library.ApplyUnreadable(ctx, scan.Item.ID, store.Unreadable{
		Title: "Scanned report", Marker: model.NoTextVersion,
		Reason: "the document has no text layer: a scan of 15 page(s)",
	}); err != nil {
		t.Fatalf("ApplyUnreadable(no text): %v", err)
	}

	rec := itemsRequest(h, http.MethodGet, "/api/items/"+scan.Item.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET item = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"noText":true`) {
		t.Errorf("scanned document = %s, want the noText flag", body)
	}
	if strings.Contains(body, `"walled":true`) {
		t.Errorf("scanned document = %s, want it distinguished from a wall", body)
	}
	if !strings.Contains(body, `"state":"ready"`) {
		t.Errorf("scanned document = %s, want state ready", body)
	}
	if !strings.Contains(body, `"title":"Scanned report"`) {
		t.Errorf("scanned document = %s, want the title it was filed with", body)
	}

	rec = itemsRequest(h, http.MethodGet, "/api/attention", nil)
	if body := rec.Body.String(); !strings.Contains(body, `"items":[]`) {
		t.Errorf("attention = %s, want the scan out of it", body)
	}
}
