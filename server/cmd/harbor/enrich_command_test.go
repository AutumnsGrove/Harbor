package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// `harbor enrich` is the path from a --no-enrich import to the enrichment loop
// (plans/05, "Volume vs the enrichment queue"). It queues; it never calls the
// model itself.

// enrichEnv is exportEnv with the model key the command insists on.
func enrichEnv(t *testing.T, dataDir, key string) func(string) string {
	t.Helper()
	base := exportEnv(t, dataDir)
	return func(name string) string {
		if name == build.EnvOpenRouterAPIKey {
			return key
		}
		return base(name)
	}
}

// seedImportBacklog writes what a bulk import leaves behind: two ready items the
// model has never read.
func seedImportBacklog(t *testing.T, dataDir string) *store.Store {
	t.Helper()
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	for _, id := range []string{"one", "two"} {
		item, err := library.CreateItem(context.Background(), store.ItemInput{
			ID: id, Note: "from the export", Source: model.SourceImport, Priority: -1,
		})
		if err != nil {
			t.Fatalf("CreateItem(%s): %v", id, err)
		}
		if err := library.MarkReady(context.Background(), item.ID); err != nil {
			t.Fatalf("MarkReady(%s): %v", id, err)
		}
	}
	return library
}

func TestEnrichCommandQueuesTheBacklog(t *testing.T) {
	dataDir := t.TempDir()
	library := seedImportBacklog(t, dataDir)
	defer library.Close()

	var out bytes.Buffer
	if err := run(context.Background(), []string{"enrich"}, enrichEnv(t, dataDir, "test-key"), &out); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if !strings.Contains(out.String(), "queued 2 item(s)") {
		t.Errorf("printed %q, want the count", out.String())
	}

	for _, id := range []string{"one", "two"} {
		item, err := library.GetItem(context.Background(), id)
		if err != nil {
			t.Fatalf("GetItem(%s): %v", id, err)
		}
		if item.State != model.StateQueued {
			t.Errorf("%s = %q, want queued", id, item.State)
		}
		if item.Priority != -1 {
			t.Errorf("%s priority = %d, want -1 so a hand save outranks it", id, item.Priority)
		}
	}
}

// A second run has nothing to do and says so rather than printing a zero.
func TestEnrichCommandIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	library := seedImportBacklog(t, dataDir)
	defer library.Close()

	env := enrichEnv(t, dataDir, "test-key")
	if err := run(context.Background(), []string{"enrich"}, env, &bytes.Buffer{}); err != nil {
		t.Fatalf("first enrich: %v", err)
	}

	var second bytes.Buffer
	if err := run(context.Background(), []string{"enrich"}, env, &second); err != nil {
		t.Fatalf("second enrich: %v", err)
	}
	if !strings.Contains(second.String(), "no items are waiting") {
		t.Errorf("second run printed %q, want nothing to do", second.String())
	}
}

// The dry run reports and writes nothing: it is what gets run before spending.
func TestEnrichCommandDryRunWritesNothing(t *testing.T) {
	dataDir := t.TempDir()
	library := seedImportBacklog(t, dataDir)
	defer library.Close()

	var out bytes.Buffer
	if err := run(context.Background(), []string{"enrich", "--dry-run"}, enrichEnv(t, dataDir, "test-key"), &out); err != nil {
		t.Fatalf("enrich --dry-run: %v", err)
	}
	if !strings.Contains(out.String(), "would queue 2 item(s)") {
		t.Errorf("printed %q, want the count", out.String())
	}
	item, err := library.GetItem(context.Background(), "one")
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if item.State != model.StateReady {
		t.Errorf("state = %q after a dry run, want ready", item.State)
	}
}

// The dry run has to answer what the real run would do, --limit included: a
// batch is checked with `--dry-run --limit N` before it is paid for.
func TestEnrichCommandDryRunHonoursLimit(t *testing.T) {
	dataDir := t.TempDir()
	library := seedImportBacklog(t, dataDir)
	defer library.Close()

	var out bytes.Buffer
	if err := run(context.Background(), []string{"enrich", "--dry-run", "--limit", "1"}, enrichEnv(t, dataDir, "test-key"), &out); err != nil {
		t.Fatalf("enrich --dry-run --limit 1: %v", err)
	}
	if !strings.Contains(out.String(), "would queue 1 item(s)") {
		t.Errorf("printed %q, want one item, the same as the real run", out.String())
	}
	if !strings.Contains(out.String(), "2 waiting in total") {
		t.Errorf("printed %q, want the size of the whole backlog too", out.String())
	}
	for _, id := range []string{"one", "two"} {
		item, err := library.GetItem(context.Background(), id)
		if err != nil {
			t.Fatalf("GetItem(%s): %v", id, err)
		}
		if item.State != model.StateReady {
			t.Errorf("%s = %q after a dry run, want ready", id, item.State)
		}
	}
}

// Without a key the loop would mark rows ready in a second and enrich nothing,
// so the command refuses rather than filling the library with a lie.
func TestEnrichCommandRefusesWithoutAKey(t *testing.T) {
	dataDir := t.TempDir()
	library := seedImportBacklog(t, dataDir)
	defer library.Close()

	err := run(context.Background(), []string{"enrich"}, enrichEnv(t, dataDir, ""), &bytes.Buffer{})
	if err == nil {
		t.Fatal("enrich succeeded without a model key")
	}
	if !strings.Contains(err.Error(), build.EnvOpenRouterAPIKey) {
		t.Errorf("error %q does not name the missing variable", err)
	}
	item, getErr := library.GetItem(context.Background(), "one")
	if getErr != nil {
		t.Fatalf("GetItem: %v", getErr)
	}
	if item.State != model.StateReady {
		t.Errorf("state = %q after a refusal, want ready: nothing may be queued", item.State)
	}
}

// --limit drains a big backlog in batches, oldest saved first.
func TestEnrichCommandLimit(t *testing.T) {
	dataDir := t.TempDir()
	library := seedImportBacklog(t, dataDir)
	defer library.Close()

	var out bytes.Buffer
	if err := run(context.Background(), []string{"enrich", "--limit", "1"}, enrichEnv(t, dataDir, "test-key"), &out); err != nil {
		t.Fatalf("enrich --limit 1: %v", err)
	}
	if !strings.Contains(out.String(), "queued 1 item(s)") || !strings.Contains(out.String(), "1 still waiting") {
		t.Errorf("printed %q, want one queued and one still waiting", out.String())
	}
}
