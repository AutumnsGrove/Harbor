package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/AutumnsGrove/Harbor/contracts"
	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// plans/02 test 14 — replay reads the Worker's done jobs and re-ingests them.
// A fake Worker records the request and serves the contract's own fixture, so
// the CLI is proven against the same shape the claim path validates.

const replayedJob = `{
	"id": "job-1", "source": "shortcut", "kind": "link",
	"link": "https://example.com/replayed", "note": "from the Worker",
	"title": null, "group_key": null, "created_at": 1795113823,
	"status": "done", "attempts": 1, "last_error": "", "blobs": []
}`

func replayEnv(t *testing.T, workerURL string) map[string]string {
	t.Helper()
	return map[string]string{
		build.EnvDevNoAuth:   "1",
		build.EnvDataDir:     t.TempDir(),
		build.EnvUpdateDir:   t.TempDir(),
		build.EnvWorkerURL:   workerURL,
		build.EnvWorkerToken: "potato-token",
	}
}

func TestReplayCommandIngestsDoneJobs(t *testing.T) {
	var gotQuery string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/jobs/replay"):
			gotQuery = r.URL.RawQuery
			fmt.Fprintf(w, `{"since":1,"jobs":[%s]}`, replayedJob)
		case strings.HasSuffix(r.URL.Path, "/done"):
			fmt.Fprint(w, `{"status":"ok"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not found"}`)
		}
	}))
	defer worker.Close()

	values := replayEnv(t, worker.URL)
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"replay", "--since", "1h"}, env(values), &stdout); err != nil {
		t.Fatalf("run(replay) error = %v", err)
	}
	if !strings.Contains(stdout.String(), "1 new") {
		t.Errorf("output = %q, want one new item", stdout.String())
	}
	if !strings.Contains(gotQuery, "since=") {
		t.Errorf("replay query = %q, want a since", gotQuery)
	}

	library, err := store.Open(values[build.EnvDataDir])
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()
	items, err := library.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("%d items after replay, want 1", len(items))
	}
	if items[0].URL == nil || *items[0].URL != "https://example.com/replayed" {
		t.Errorf("url = %v, want the replayed link", items[0].URL)
	}
}

// Replaying the same window twice is the safe operation design.md §5 promises.
func TestReplayCommandTwiceChangesNothing(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/jobs/replay") {
			fmt.Fprintf(w, `{"since":1,"jobs":[%s]}`, replayedJob)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer worker.Close()

	values := replayEnv(t, worker.URL)
	for run := 1; run <= 2; run++ {
		var stdout bytes.Buffer
		if err := replayCommand(context.Background(), env(values), []string{"--since", "1h"}, &stdout); err != nil {
			t.Fatalf("replay %d: %v", run, err)
		}
		if run == 2 && !strings.Contains(stdout.String(), "1 already ingested") {
			t.Errorf("second replay output = %q, want already ingested", stdout.String())
		}
	}

	library, err := store.Open(values[build.EnvDataDir])
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()
	items, err := library.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("%d items after replaying twice, want 1", len(items))
	}
}

func TestReplayNeedsAWorker(t *testing.T) {
	values := replayEnv(t, "")
	delete(values, build.EnvWorkerURL)
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"replay", "--since", "1h"}, env(values), &stdout)
	if err == nil || !strings.Contains(err.Error(), build.EnvWorkerURL) {
		t.Fatalf("run(replay) error = %v, want it to name the missing Worker URL", err)
	}
}

func TestParseSince(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }

	tests := []struct {
		name string
		args []string
		want time.Time
	}{
		{name: "duration", args: []string{"--since", "90m"}, want: now().Add(-90 * time.Minute)},
		{name: "equals form", args: []string{"--since=2h"}, want: now().Add(-2 * time.Hour)},
		{name: "single dash", args: []string{"-since=1h"}, want: now().Add(-time.Hour)},
		{name: "RFC3339", args: []string{"--since", "2026-11-19T08:30:00Z"}, want: time.Date(2026, 11, 19, 8, 30, 0, 0, time.UTC)},
		{name: "date only", args: []string{"--since", "2026-11-19"}, want: time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)},
		{name: "unix seconds", args: []string{"--since", "1795000000"}, want: time.Unix(1795000000, 0).UTC()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSince(test.args, now)
			if err != nil {
				t.Fatalf("parseSince(%v): %v", test.args, err)
			}
			if !got.Equal(test.want) {
				t.Errorf("parseSince(%v) = %v, want %v", test.args, got, test.want)
			}
		})
	}

	bad := [][]string{
		nil,
		{"--since"},
		{"--since", ""},
		{"--since", "yesterday"},
		{"--since", "-1h"},
		{"--since", "0"},
		{"--since", "1h", "--force"},
		{"1h"},
	}
	for _, args := range bad {
		if _, err := parseSince(args, now); err == nil {
			t.Errorf("parseSince(%v) error = nil, want a rejection", args)
		}
	}
}

// The contract fixture must be a job the replay path accepts; if someone edits
// the fixture into a shape the ingest stage cannot read, this test is the loud
// one.
func TestReplayedJobMatchesTheContract(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(replayedJob))
	if err != nil {
		t.Fatalf("the fixture is not JSON: %v", err)
	}
	if err := contracts.Validate(contracts.Job, doc); err != nil {
		t.Fatalf("the replay fixture does not match contracts/job.schema.json: %v", err)
	}
}
