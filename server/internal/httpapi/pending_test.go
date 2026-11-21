package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
)

// plans/02 "The pending window" — the greyed cards are fed by the Worker's
// read-only list, and the page never claims anything. The fake is the only part
// that talks to the Worker.

type fakePending struct {
	jobs []workerclient.Job
	err  error
}

func (f fakePending) Pending(context.Context) ([]workerclient.Job, error) {
	return f.jobs, f.err
}

func ptr(s string) *string { return &s }

func pendingHandler(t *testing.T, source PendingSource) http.Handler {
	t.Helper()
	return New(testConfig(t), slog.New(slog.DiscardHandler), testAssets(), WithPending(source)).Handler()
}

func decodePending(t *testing.T, h http.Handler) (int, pendingResponse) {
	t.Helper()
	rec := authed(h, http.MethodGet, "/api/pending")
	var body pendingResponse
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("pending response is not JSON: %v (%s)", err, rec.Body.String())
		}
	}
	return rec.Code, body
}

func TestPendingListsWhatIsStillAtTheWorker(t *testing.T) {
	source := fakePending{jobs: []workerclient.Job{
		{
			ID: "job-1", Source: "shortcut", Kind: "link",
			Link: ptr("https://example.com/a"), CreatedAt: 1795113823, Status: "queued",
		},
		{
			ID: "job-2", Source: "telegram", Kind: "note", Title: ptr("a title"),
			Note: ptr("the note"), CreatedAt: 1795113824, Status: "claimed",
		},
	}}
	code, body := decodePending(t, pendingHandler(t, source))
	if code != http.StatusOK {
		t.Fatalf("GET /api/pending = %d, want 200", code)
	}
	if len(body.Jobs) != 2 {
		t.Fatalf("got %d pending jobs, want 2", len(body.Jobs))
	}
	first := body.Jobs[0]
	if first.Title != "https://example.com/a" {
		t.Errorf("title = %q, want the link until the page is read", first.Title)
	}
	if first.CreatedAt != time.Unix(1795113823, 0).UTC() {
		t.Errorf("createdAt = %v, want the job's save time", first.CreatedAt)
	}
	second := body.Jobs[1]
	if second.Title != "a title" || second.Kind != "note" || second.Source != "telegram" {
		t.Errorf("second job = %+v", second)
	}
}

func TestPendingFallsBackToTheNote(t *testing.T) {
	code, body := decodePending(t, pendingHandler(t, fakePending{jobs: []workerclient.Job{{
		ID: "job-1", Source: "telegram", Kind: "note",
		Note: ptr("first line\nsecond line"), CreatedAt: 1,
	}}}))
	if code != http.StatusOK || len(body.Jobs) != 1 {
		t.Fatalf("GET /api/pending = %d, %+v", code, body)
	}
	if body.Jobs[0].Title != "first line" {
		t.Errorf("title = %q, want the note's first line", body.Jobs[0].Title)
	}
}

// No Worker means nothing is pending, not an error: `make dev` has no queue.
func TestPendingWithoutAWorkerIsEmpty(t *testing.T) {
	code, body := decodePending(t, pendingHandler(t, nil))
	if code != http.StatusOK {
		t.Fatalf("GET /api/pending = %d, want 200", code)
	}
	if len(body.Jobs) != 0 {
		t.Errorf("got %d jobs with no Worker configured, want 0", len(body.Jobs))
	}
}

// An outage is reported, but the page still renders the library around it.
func TestPendingReportsAWorkerOutage(t *testing.T) {
	code, _ := decodePending(t, pendingHandler(t, fakePending{err: errors.New("network down")}))
	if code != http.StatusBadGateway {
		t.Errorf("GET /api/pending with a broken Worker = %d, want 502", code)
	}
}

func TestPendingIsBehindTheIdentityLock(t *testing.T) {
	h := pendingHandler(t, nil)
	rec := request(h, http.MethodGet, "/api/pending", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/pending without an identity = %d, want 403", rec.Code)
	}
}
