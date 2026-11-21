package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// plans/02 tests 12–16 — the in-app save path, the home list and its cursor,
// the item page's edit, soft delete + restore, and the composer's merge lookup.

func itemsHandler(t *testing.T) (http.Handler, *store.Store) {
	t.Helper()
	cfg := testConfig(t)
	library, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = library.Close() })
	h := New(cfg, slog.New(slog.DiscardHandler), testAssets(), WithStore(library)).Handler()
	return h, library
}

func itemsRequest(h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			panic(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Host = "localhost"
	req.Header.Set(identityHeader, "autumn@grove.place")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// save decodes the create response into the fields the assertions use.
type savedItem struct {
	Item struct {
		ID          string  `json:"id"`
		URL         *string `json:"url"`
		URLOriginal string  `json:"urlOriginal"`
		Title       string  `json:"title"`
		Note        string  `json:"note"`
		Kind        string  `json:"kind"`
		KindSource  string  `json:"kindSource"`
		Source      string  `json:"source"`
		ReadLater   bool    `json:"readLater"`
		DeletedAt   *time.Time
	} `json:"item"`
	Merged bool `json:"merged"`
}

func saveItem(t *testing.T, h http.Handler, body map[string]any) savedItem {
	t.Helper()
	rec := itemsRequest(h, http.MethodPost, "/api/items", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/items = %d (body %s), want 201", rec.Code, rec.Body.String())
	}
	var saved savedItem
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	return saved
}

func TestCreateItemSavesACanonicalLink(t *testing.T) {
	h, library := itemsHandler(t)
	saved := saveItem(t, h, map[string]any{
		"url":       "https://Example.com/a?utm_source=x#frag",
		"note":      "read this",
		"readLater": true,
	})

	if saved.Item.ID == "" {
		t.Error("the created item has no id")
	}
	if saved.Merged {
		t.Error("the first save reported a merge")
	}
	if saved.Item.URL == nil || *saved.Item.URL != "https://example.com/a" {
		t.Errorf("url = %v, want the canonical URL", saved.Item.URL)
	}
	if saved.Item.URLOriginal != "https://Example.com/a?utm_source=x#frag" {
		t.Errorf("urlOriginal = %q, want what was pasted", saved.Item.URLOriginal)
	}
	if saved.Item.Title != "https://example.com/a" {
		t.Errorf("title = %q, want the URL fallback", saved.Item.Title)
	}
	if saved.Item.Kind != "article" || saved.Item.KindSource != "default" {
		t.Errorf("kind = %q/%q, want article/default", saved.Item.Kind, saved.Item.KindSource)
	}
	if saved.Item.Source != "app" {
		t.Errorf("source = %q, want app", saved.Item.Source)
	}
	if !saved.Item.ReadLater {
		t.Error("readLater is false; the save asked for it")
	}

	// The row is really in the library, not only in the response.
	items, err := library.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 || items[0].ID != saved.Item.ID {
		t.Errorf("library holds %d items, want the saved one", len(items))
	}
}

// plans/02 test 12 and save path demo step 2 — a second save merges and says so.
func TestCreateItemMergesAReSavedURL(t *testing.T) {
	h, library := itemsHandler(t)
	first := saveItem(t, h, map[string]any{"url": "https://example.com/a", "note": "one"})
	second := saveItem(t, h, map[string]any{"url": "https://EXAMPLE.com/a", "note": "two", "title": "A title"})

	if !second.Merged {
		t.Error("the re-save reported merged=false")
	}
	if second.Item.ID != first.Item.ID {
		t.Errorf("second save created %q, want the first item %q", second.Item.ID, first.Item.ID)
	}
	if !strings.Contains(second.Item.Note, "one") || !strings.Contains(second.Item.Note, "two") {
		t.Errorf("merged note = %q, want both saves", second.Item.Note)
	}
	if second.Item.Title != "A title" {
		t.Errorf("title = %q, want the re-save's title", second.Item.Title)
	}
	items, err := library.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("%d items after re-saving one URL, want 1", len(items))
	}
}

// plans/02 test 15 — a URL-less save is a note, and it never has kind "unknown".
func TestCreateItemSavesANote(t *testing.T) {
	h, _ := itemsHandler(t)
	saved := saveItem(t, h, map[string]any{"note": "a thought"})
	if saved.Item.Kind != "note" {
		t.Errorf("kind = %q, want note", saved.Item.Kind)
	}
	if saved.Item.URL != nil {
		t.Errorf("url = %v, want null", *saved.Item.URL)
	}
}

func TestCreateItemRejectsBadSaves(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want int
	}{
		{name: "empty", body: map[string]any{}, want: http.StatusBadRequest},
		{name: "a URL that cannot be canonicalised", body: map[string]any{"url": "ftp://example.com/x"}, want: http.StatusUnprocessableEntity},
		{name: "a URL without a scheme", body: map[string]any{"url": "example.com/x"}, want: http.StatusUnprocessableEntity},
		{name: "a door source", body: map[string]any{"note": "x", "source": "telegram"}, want: http.StatusBadRequest},
		{name: "an import source", body: map[string]any{"note": "x", "source": "import"}, want: http.StatusBadRequest},
		{name: "an unknown field", body: map[string]any{"note": "x", "nope": true}, want: http.StatusBadRequest},
	}
	h, _ := itemsHandler(t)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := itemsRequest(h, http.MethodPost, "/api/items", test.body)
			if rec.Code != test.want {
				t.Errorf("POST /api/items = %d, want %d (body %s)", rec.Code, test.want, rec.Body.String())
			}
		})
	}
}

func TestListItemsPagesNewestFirst(t *testing.T) {
	h, library := itemsHandler(t)
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for n := 0; n < 5; n++ {
		if _, err := library.CreateItem(context.Background(), store.ItemInput{
			ID:      fmt.Sprintf("i%d", n),
			Note:    fmt.Sprintf("note %d", n),
			SavedAt: base.Add(time.Duration(n) * time.Minute),
		}); err != nil {
			t.Fatalf("CreateItem: %v", err)
		}
	}

	type listBody struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		Next *string `json:"next"`
	}
	page := func(target string) listBody {
		t.Helper()
		rec := itemsRequest(h, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d (body %s)", target, rec.Code, rec.Body.String())
		}
		var body listBody
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("list response is not JSON: %v", err)
		}
		return body
	}

	first := page("/api/items?limit=2")
	if len(first.Items) != 2 || first.Items[0].ID != "i4" || first.Items[1].ID != "i3" {
		t.Fatalf("first page = %+v, want i4, i3", first.Items)
	}
	if first.Next == nil {
		t.Fatal("first page has no cursor, want one")
	}

	second := page("/api/items?limit=2&cursor=" + *first.Next)
	if len(second.Items) != 2 || second.Items[0].ID != "i2" || second.Items[1].ID != "i1" {
		t.Fatalf("second page = %+v, want i2, i1", second.Items)
	}

	third := page("/api/items?limit=2&cursor=" + *second.Next)
	if len(third.Items) != 1 || third.Items[0].ID != "i0" {
		t.Fatalf("third page = %+v, want i0", third.Items)
	}
	if third.Next != nil {
		t.Errorf("last page has a cursor %q, want none", *third.Next)
	}
}

func TestListItemsRejectsABadCursorOrLimit(t *testing.T) {
	h, _ := itemsHandler(t)
	for _, target := range []string{
		"/api/items?limit=0",
		"/api/items?limit=1000",
		"/api/items?limit=many",
		"/api/items?cursor=not-a-cursor",
	} {
		rec := itemsRequest(h, http.MethodGet, target, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", target, rec.Code)
		}
	}
}

// plans/02 test 6's HTTP half — delete hides from home and appears in Recently
// deleted; restore brings it back.
func TestDeleteRestoreAndTheDeletedView(t *testing.T) {
	h, _ := itemsHandler(t)
	live := saveItem(t, h, map[string]any{"note": "live"})
	gone := saveItem(t, h, map[string]any{"note": "gone"})

	if rec := itemsRequest(h, http.MethodDelete, "/api/items/"+gone.Item.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}

	rec := itemsRequest(h, http.MethodGet, "/api/items", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"`+live.Item.ID+`"`) {
		t.Errorf("home list after delete = %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), gone.Item.ID) {
		t.Error("the deleted item is still in the home list")
	}

	rec = itemsRequest(h, http.MethodGet, "/api/items?deleted=true", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), gone.Item.ID) || strings.Contains(rec.Body.String(), live.Item.ID) {
		t.Errorf("Recently deleted = %s, want only the deleted item", rec.Body.String())
	}

	// By id, a deleted item is still readable so its page can offer Restore.
	rec = itemsRequest(h, http.MethodGet, "/api/items/"+gone.Item.ID, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"deletedAt":"`) {
		t.Errorf("GET a deleted item = %d %s", rec.Code, rec.Body.String())
	}

	if rec := itemsRequest(h, http.MethodPost, "/api/items/"+gone.Item.ID+"/restore", nil); rec.Code != http.StatusOK {
		t.Fatalf("restore = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	rec = itemsRequest(h, http.MethodGet, "/api/items", nil)
	if !strings.Contains(rec.Body.String(), gone.Item.ID) {
		t.Error("the restored item is not back in the home list")
	}
}

func TestUpdateItemPersistsAnEdit(t *testing.T) {
	h, _ := itemsHandler(t)
	saved := saveItem(t, h, map[string]any{"url": "https://example.com/a", "note": "before"})

	rec := itemsRequest(h, http.MethodPatch, "/api/items/"+saved.Item.ID, map[string]any{
		"title": "After",
		"note":  "changed",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d (body %s), want 200", rec.Code, rec.Body.String())
	}

	rec = itemsRequest(h, http.MethodGet, "/api/items/"+saved.Item.ID, nil)
	var body struct {
		Item struct {
			Title      string `json:"title"`
			Note       string `json:"note"`
			Kind       string `json:"kind"`
			KindSource string `json:"kindSource"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("item response is not JSON: %v", err)
	}
	if body.Item.Title != "After" || body.Item.Note != "changed" {
		t.Errorf("item after PATCH = %+v, want the edit persisted", body.Item)
	}

	// A manual kind is recorded as manual, and an invented one is refused.
	if rec := itemsRequest(h, http.MethodPatch, "/api/items/"+saved.Item.ID, map[string]any{"kind": "video"}); rec.Code != http.StatusOK {
		t.Fatalf("PATCH kind = %d (body %s)", rec.Code, rec.Body.String())
	}
	if rec := itemsRequest(h, http.MethodPatch, "/api/items/"+saved.Item.ID, map[string]any{"kind": "nonsense"}); rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH an invented kind = %d, want 400", rec.Code)
	}
	if rec := itemsRequest(h, http.MethodPatch, "/api/items/"+saved.Item.ID, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("PATCH with nothing = %d, want 400", rec.Code)
	}
}

func TestUnknownItemsAre404(t *testing.T) {
	h, _ := itemsHandler(t)
	for _, tc := range []struct {
		method string
		target string
		body   any
	}{
		{http.MethodGet, "/api/items/nope", nil},
		{http.MethodPatch, "/api/items/nope", map[string]any{"title": "x"}},
		{http.MethodDelete, "/api/items/nope", nil},
		{http.MethodPost, "/api/items/nope/restore", nil},
		{http.MethodGet, "/api/items/nope/other", nil},
	} {
		if rec := itemsRequest(h, tc.method, tc.target, tc.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.target, rec.Code)
		}
	}
}

func TestLookupAnswersTheMergeQuestion(t *testing.T) {
	h, _ := itemsHandler(t)
	saved := saveItem(t, h, map[string]any{"url": "https://example.com/a"})

	rec := itemsRequest(h, http.MethodGet, "/api/items/lookup?url=https%3A%2F%2FEXAMPLE.com%2Fa%3Futm_source%3Dx", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("lookup = %d (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Exists bool `json:"exists"`
		Item   *struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("lookup response is not JSON: %v", err)
	}
	if !body.Exists || body.Item == nil || body.Item.ID != saved.Item.ID {
		t.Errorf("lookup = %+v, want the existing item", body)
	}

	rec = itemsRequest(h, http.MethodGet, "/api/items/lookup?url=https%3A%2F%2Fexample.com%2Fother", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"exists":false`) {
		t.Errorf("lookup of a new URL = %d %s", rec.Code, rec.Body.String())
	}

	if rec := itemsRequest(h, http.MethodGet, "/api/items/lookup", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("lookup without a url = %d, want 400", rec.Code)
	}
	if rec := itemsRequest(h, http.MethodGet, "/api/items/lookup?url=ftp%3A%2F%2Fx", nil); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("lookup of an impossible URL = %d, want 422", rec.Code)
	}
}

// The phase-0 binary has no store; the routes must say so instead of panicking.
func TestItemsNeedALibrary(t *testing.T) {
	h := handlerFor(t, testConfig(t))
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/api/items"},
		{http.MethodPost, "/api/items"},
		{http.MethodGet, "/api/items/lookup?url=https%3A%2F%2Fexample.com"},
	} {
		if rec := authed(h, tc.method, tc.target); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.target, rec.Code)
		}
	}
}

func TestItemsRejectsAnUnknownMethod(t *testing.T) {
	h, _ := itemsHandler(t)
	if rec := itemsRequest(h, http.MethodPut, "/api/items", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /api/items = %d, want 405", rec.Code)
	}
}
