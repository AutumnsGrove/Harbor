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

// seedRelativeCovers writes finished rows the way they exist on the potato:
// enriched once, with a root-relative og:image stored before the pipeline
// resolved it (issue #67).
func seedRelativeCovers(t *testing.T, dataDir string) *store.Store {
	t.Helper()
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := context.Background()

	for id, og := range map[string]string{
		"relative": `{"image":"/images/og.png"}`,
		"protocol": `{"image":"//cdn.example/og.png"}`,
		"absolute": `{"image":"https://example.test/og.png"}`,
		"importer": `{"cover":"images/og.png","excerpt":"x"}`,
		"no-image": `{"excerpt":"words only"}`,
	} {
		item, err := library.CreateItem(ctx, store.ItemInput{ID: id, Note: id, OGJSON: og})
		if err != nil {
			t.Fatalf("CreateItem(%s): %v", id, err)
		}
		// Finished, and read by the model: the state QueueUnenriched ignores.
		if _, err := library.ApplyEnrichment(ctx, item.ID, store.Enrichment{
			Summary: "already read", Kind: "article", KindSource: "ai",
			PromptVersion: "tag.v1", ExtractVersion: "extract.v1", OGJSON: og,
		}); err != nil {
			t.Fatalf("ApplyEnrichment(%s): %v", id, err)
		}
	}
	// One row the model has never read. `--refresh` must leave it alone; the
	// backlog is a different command's job (pi-review on #68).
	backlog, err := library.CreateItem(ctx, store.ItemInput{ID: "backlog", Note: "never read"})
	if err != nil {
		t.Fatalf("CreateItem(backlog): %v", err)
	}
	if err := library.MarkReady(ctx, backlog.ID); err != nil {
		t.Fatalf("MarkReady(backlog): %v", err)
	}

	return library
}

func TestEnrichRefreshCountsOnlyUnusableCovers(t *testing.T) {
	dataDir := t.TempDir()
	library := seedRelativeCovers(t, dataDir)
	defer library.Close()

	// The trick that keeps this honest: ApplyEnrichment rewrites og_json, so the
	// relative value has to be written back after it to model a row that predates
	// the fix.
	ctx := context.Background()
	for id, og := range map[string]string{
		"relative": `{"image":"/images/og.png"}`,
		"protocol": `{"image":"//cdn.example/og.png"}`,
		"importer": `{"cover":"images/og.png","excerpt":"x"}`,
	} {
		if _, err := library.ApplyEnrichment(ctx, id, store.Enrichment{
			Summary: "already read", Kind: "article", KindSource: "ai",
			PromptVersion: "tag.v1", ExtractVersion: "extract.v1", OGJSON: og,
		}); err != nil {
			t.Fatalf("re-applying og_json for %s: %v", id, err)
		}
	}

	var dry bytes.Buffer
	if err := run(context.Background(), []string{"enrich", "--refresh", "--dry-run"}, enrichEnv(t, dataDir, "test-key"), &dry); err != nil {
		t.Fatalf("enrich --refresh --dry-run: %v", err)
	}
	if !strings.Contains(dry.String(), "would re-queue 3 item(s)") {
		t.Fatalf("dry run printed %q, want the three unusable covers", dry.String())
	}
	if !strings.Contains(dry.String(), "nothing was written") {
		t.Errorf("dry run printed %q without saying nothing was written", dry.String())
	}
	// A dry run must not have touched anything.
	count, err := library.RelativeCoverCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("after the dry run %d rows are still unusable, want 3", count)
	}

	var real bytes.Buffer
	if err := run(context.Background(), []string{"enrich", "--refresh"}, enrichEnv(t, dataDir, "test-key"), &real); err != nil {
		t.Fatalf("enrich --refresh: %v", err)
	}
	if !strings.Contains(real.String(), "re-queued 3 item(s)") {
		t.Errorf("printed %q, want the count", real.String())
	}

	for _, id := range []string{"relative", "protocol", "importer"} {
		item, err := library.GetItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.State != model.StateQueued {
			t.Errorf("%s = %q, want queued", id, item.State)
		}
	}
	// The never-enriched row is still waiting for its first pass: --refresh must
	// not have queued it, and must not have spent --limit on it.
	backlogRow, err := library.GetItem(ctx, "backlog")
	if err != nil {
		t.Fatal(err)
	}
	if backlogRow.State != model.StateReady {
		t.Errorf("backlog row = %q, want it left ready: --refresh queued the normal backlog too", backlogRow.State)
	}

	// An absolute cover and a page with no image are not this command's work.
	for _, id := range []string{"absolute", "no-image"} {
		item, err := library.GetItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.State == model.StateQueued {
			t.Errorf("%s was re-queued, but its cover is fine or absent", id)
		}
	}

	var again bytes.Buffer
	if err := run(context.Background(), []string{"enrich", "--refresh"}, enrichEnv(t, dataDir, "test-key"), &again); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if !strings.Contains(again.String(), "no finished item holds a cover") {
		t.Errorf("second run printed %q, want nothing to do", again.String())
	}
}

// issue #76: a rule that files a wall or a dead link cannot help the rows an
// older rule already parked, so this selection exists to give them back.
func TestEnrichRefileFailedRequeuesOnlyParkedRows(t *testing.T) {
	dataDir := t.TempDir()
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"parked", "other"} {
		item, err := library.CreateItem(ctx, store.ItemInput{ID: id, Note: id})
		if err != nil {
			t.Fatalf("CreateItem(%s): %v", id, err)
		}
		_ = item
	}
	// One parked row, one that is already filed and must be left alone.
	if _, err := library.RecordEnrichmentFailure(ctx, "parked", "answered 403", nil); err != nil {
		t.Fatal(err)
	}
	if err := library.MarkReady(ctx, "other"); err != nil {
		t.Fatal(err)
	}

	// The dry run reports and writes nothing.
	var dry bytes.Buffer
	if err := run(ctx, []string{"enrich", "--refile-failed", "--dry-run"},
		enrichEnv(t, dataDir, "test-key"), &dry); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(dry.String(), "would re-queue 1 parked item") {
		t.Errorf("dry run said %q", dry.String())
	}
	// The number it prints has to be the number the real run writes; it said -1
	// here while the run queued at 0 (caught on the box, 2026-11-26).
	if !strings.Contains(dry.String(), "priority 0") {
		t.Errorf("dry run promised the wrong priority: %q", dry.String())
	}
	parked, err := library.GetItem(ctx, "parked")
	if err != nil {
		t.Fatal(err)
	}
	if parked.State != model.StateFailed {
		t.Fatalf("the dry run changed the row to %q", parked.State)
	}

	var out bytes.Buffer
	if err := run(ctx, []string{"enrich", "--refile-failed"}, enrichEnv(t, dataDir, "test-key"), &out); err != nil {
		t.Fatalf("enrich --refile-failed: %v", err)
	}
	parked, err = library.GetItem(ctx, "parked")
	if err != nil {
		t.Fatal(err)
	}
	if parked.State != model.StateQueued || parked.Attempts != 0 || parked.NextAttemptAt != nil || parked.LastError != "" {
		t.Errorf("parked row after refile = %q, attempts %d, next %v, error %q",
			parked.State, parked.Attempts, parked.NextAttemptAt, parked.LastError)
	}
	other, err := library.GetItem(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if other.State != model.StateReady {
		t.Errorf("a filed row was re-queued: state %q", other.State)
	}
	if !strings.Contains(out.String(), "without a model call") {
		t.Errorf("the command did not say most of these cost nothing: %q", out.String())
	}
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}
}

// Two selections at once would spend --limit twice on different rows.
func TestEnrichRefusesRefreshAndRefileTogether(t *testing.T) {
	if err := run(context.Background(), []string{"enrich", "--refresh", "--refile-failed"},
		enrichEnv(t, t.TempDir(), "test-key"), &bytes.Buffer{}); err == nil {
		t.Fatal("both selections were accepted")
	}
}
