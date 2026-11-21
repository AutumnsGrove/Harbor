package workerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// plans/02 test 8 and 11 — the Worker API as the potato sees it. The fake below
// is an httptest server, not a hand-built client: what is under test is the
// request we send and the validation we do on the answer.

const linkJob = `{
	"id": "job-1", "source": "shortcut", "kind": "link",
	"link": "https://example.com/x", "note": null, "title": null,
	"group_key": null, "created_at": 1795113823, "blobs": []
}`

const noteJob = `{
	"id": "job-2", "source": "telegram", "kind": "note",
	"link": null, "note": "a thought", "title": null,
	"group_key": null, "created_at": 1795113824, "blobs": []
}`

type request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// fakeWorker records every request and answers with respond. Callers can also
// use it as a plain handler when they need a different answer per call.
type fakeWorker struct {
	mu       sync.Mutex
	requests []request
	server   *httptest.Server
}

func newFakeWorker(t *testing.T, respond http.HandlerFunc) *fakeWorker {
	t.Helper()
	f := &fakeWorker{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, request{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Header: r.Header.Clone(),
			Body:   string(body),
		})
		f.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeWorker) calls() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

func jobsBody(jobs ...string) string {
	return `{"jobs":[` + strings.Join(jobs, ",") + `]}`
}

func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := New(base, "potato-token", "r42.abc1234")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNewValidatesTheBaseURLAndToken(t *testing.T) {
	t.Run("accepts a normal URL and trims a trailing slash", func(t *testing.T) {
		c, err := New("https://harbor.example.workers.dev/", "token", "r1")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.base != "https://harbor.example.workers.dev" {
			t.Errorf("base = %q, want no trailing slash", c.base)
		}
		if !strings.Contains(c.userAgent, "Harbor/r1") {
			t.Errorf("User-Agent = %q, want the version", c.userAgent)
		}
	})

	for _, tc := range []struct{ name, base, token string }{
		{"no token", "https://x.workers.dev", "  "},
		{"no scheme", "x.workers.dev", "token"},
		{"ftp", "ftp://x.workers.dev", "token"},
		{"no host", "https://", "token"},
		{"a query", "https://x.workers.dev/?a=b", "token"},
		{"a fragment", "https://x.workers.dev/#a", "token"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			if _, err := New(tc.base, tc.token, "r1"); err == nil {
				t.Fatalf("New(%q, %q) error = nil", tc.base, tc.token)
			}
		})
	}
}

func TestClaimSendsTheTokenLimitAndKinds(t *testing.T) {
	worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, jobsBody(linkJob, noteJob))
	})
	c := newTestClient(t, worker.server.URL)

	jobs, err := c.Claim(context.Background(), 5, []string{KindLink, KindNote})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}
	if jobs[0].ID != "job-1" || jobs[1].ID != "job-2" {
		t.Errorf("job ids = %q, %q", jobs[0].ID, jobs[1].ID)
	}
	if jobs[0].Link == nil || *jobs[0].Link != "https://example.com/x" {
		t.Errorf("job-1 link = %v", jobs[0].Link)
	}
	if jobs[1].Note == nil || *jobs[1].Note != "a thought" {
		t.Errorf("job-2 note = %v", jobs[1].Note)
	}

	call := worker.calls()[0]
	if call.Method != http.MethodPost || call.Path != "/jobs/claim" {
		t.Errorf("%s %s, want POST /jobs/claim", call.Method, call.Path)
	}
	if call.Query != "kinds=link%2Cnote&limit=5" {
		t.Errorf("query = %q, want kinds and limit", call.Query)
	}
	if got := call.Header.Get("Authorization"); got != "Bearer potato-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := call.Header.Get("User-Agent"); !strings.Contains(got, "Harbor/r42.abc1234") {
		t.Errorf("User-Agent = %q", got)
	}
}

// The schema is the contract: a job that does not match it must fail the whole
// batch rather than become a half-filled item.
func TestClaimRejectsAJobThatBreaksTheContract(t *testing.T) {
	cases := map[string]string{
		"unknown kind":   `{"id":"j","source":"shortcut","kind":"video","created_at":1,"blobs":[]}`,
		"unknown source": `{"id":"j","source":"email","kind":"link","created_at":1,"blobs":[]}`,
		"no id":          `{"id":"","source":"shortcut","kind":"link","created_at":1,"blobs":[]}`,
		"no blobs":       `{"id":"j","source":"shortcut","kind":"link","created_at":1}`,
		"bad blob":       `{"id":"j","source":"shortcut","kind":"upload","created_at":1,"blobs":[{"n":-1,"key":"k","content_type":"image/png","bytes":1}]}`,
	}
	for name, job := range cases {
		t.Run(name, func(t *testing.T) {
			worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, jobsBody(job))
			})
			c := newTestClient(t, worker.server.URL)
			if _, err := c.Claim(context.Background(), 5, nil); err == nil {
				t.Fatalf("Claim accepted %s", name)
			} else if !strings.Contains(err.Error(), "contracts/job.schema.json") {
				t.Errorf("error %q does not name the contract", err)
			}
		})
	}
}

func TestClaimRejectsAnAnswerWithoutAJobsArray(t *testing.T) {
	for _, body := range []string{`{}`, `{"jobs":null}`, `[]`, `not json`} {
		worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, body)
		})
		c := newTestClient(t, worker.server.URL)
		if _, err := c.Claim(context.Background(), 5, nil); err == nil {
			t.Errorf("Claim accepted the answer %q", body)
		}
	}
}

func TestNon2xxBecomesATypedError(t *testing.T) {
	t.Run("401 is ErrUnauthorized", func(t *testing.T) {
		worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"unauthorized"}`)
		})
		_, err := newTestClient(t, worker.server.URL).Claim(context.Background(), 5, nil)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("error = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("403 is ErrUnauthorized too", func(t *testing.T) {
		worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})
		_, err := newTestClient(t, worker.server.URL).Claim(context.Background(), 5, nil)
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("error = %v, want ErrUnauthorized", err)
		}
	})

	t.Run("429 carries Retry-After", func(t *testing.T) {
		worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "17")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":"slow down"}`)
		})
		_, err := newTestClient(t, worker.server.URL).Claim(context.Background(), 5, nil)
		var apiErr *Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *Error", err)
		}
		if apiErr.Status != http.StatusTooManyRequests || apiErr.RetryAfter != 17*time.Second {
			t.Errorf("error = %+v, want 429 and 17s", apiErr)
		}
		if errors.Is(err, ErrUnauthorized) {
			t.Error("a 429 matched ErrUnauthorized")
		}
	})

	t.Run("500 carries the Worker's message", func(t *testing.T) {
		worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":"internal"}`)
		})
		_, err := newTestClient(t, worker.server.URL).Claim(context.Background(), 5, nil)
		if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "internal") {
			t.Errorf("error = %v, want status and message", err)
		}
	})
}

func TestDoneAndFailedNameTheJob(t *testing.T) {
	worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	c := newTestClient(t, worker.server.URL)

	if err := c.Done(context.Background(), "job-1"); err != nil {
		t.Fatalf("Done: %v", err)
	}
	if err := c.Failed(context.Background(), "job-2", "bad envelope"); err != nil {
		t.Fatalf("Failed: %v", err)
	}

	calls := worker.calls()
	if calls[0].Method != http.MethodPost || calls[0].Path != "/jobs/job-1/done" {
		t.Errorf("first call = %s %s", calls[0].Method, calls[0].Path)
	}
	if calls[1].Path != "/jobs/job-2/failed" {
		t.Errorf("second call path = %q", calls[1].Path)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[1].Body), &body); err != nil {
		t.Fatalf("failed body is not JSON: %v", err)
	}
	if body["error"] != "bad envelope" {
		t.Errorf("failed body = %v, want the reason", body)
	}
}

func TestReplaySendsSinceAsUnixSeconds(t *testing.T) {
	worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, jobsBody(linkJob))
	})
	c := newTestClient(t, worker.server.URL)
	since := time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)
	if _, err := c.Replay(context.Background(), since); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	call := worker.calls()[0]
	if call.Method != http.MethodGet || call.Path != "/jobs/replay" {
		t.Errorf("%s %s, want GET /jobs/replay", call.Method, call.Path)
	}
	if want := fmt.Sprintf("since=%d", since.Unix()); call.Query != want {
		t.Errorf("query = %q, want %q", call.Query, want)
	}
}

// A response larger than the cap is refused instead of buffered; the cap is one
// invariant-7 guard (every external call has a byte cap).
func TestAnOverlongResponseIsRefused(t *testing.T) {
	previous := maxResponseBytes
	maxResponseBytes = 64
	t.Cleanup(func() { maxResponseBytes = previous })

	worker := newFakeWorker(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, jobsBody(linkJob))
	})
	_, err := newTestClient(t, worker.server.URL).Claim(context.Background(), 5, nil)
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("error = %v, want a byte-cap error", err)
	}
}

func TestACancelledContextStopsTheCall(t *testing.T) {
	block := make(chan struct{})
	worker := newFakeWorker(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(block) })

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newTestClient(t, worker.server.URL).Claim(ctx, 5, nil)
	if err == nil {
		t.Fatal("Claim error = nil despite a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("call took %v after the context expired", elapsed)
	}
}
