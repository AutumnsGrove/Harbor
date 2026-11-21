package queue

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
)

// plans/02 tests 8 and 9 — claim → ingest → done, the order of the network
// calls, and what happens when one of them fails. The store is real SQLite; the
// Worker is a hand-written fake, per testing.md ("no mocking the thing under
// test").

type fakeWorker struct {
	mu       sync.Mutex
	batches  [][]workerclient.Job
	claimErr error
	claims   int
	kinds    [][]string
	limits   []int

	done      []string
	doneTries int
	doneErrs  []error
	doneHook  func(context.Context, string)

	failed    map[string]string
	failedErr error
}

func (f *fakeWorker) Claim(_ context.Context, limit int, kinds []string) ([]workerclient.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	f.limits = append(f.limits, limit)
	f.kinds = append(f.kinds, append([]string(nil), kinds...))
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return batch, nil
}

func (f *fakeWorker) Done(ctx context.Context, id string) error {
	if f.doneHook != nil {
		f.doneHook(ctx, id)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doneTries++
	if len(f.doneErrs) > 0 {
		err := f.doneErrs[0]
		f.doneErrs = f.doneErrs[1:]
		if err != nil {
			return err
		}
	}
	f.done = append(f.done, id)
	return nil
}

func (f *fakeWorker) Failed(_ context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failed == nil {
		f.failed = map[string]string{}
	}
	f.failed[id] = reason
	return f.failedErr
}

func (f *fakeWorker) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

func (f *fakeWorker) doneCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.done)
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ptr(s string) *string { return &s }

func linkJob(id, link, note string) workerclient.Job {
	return workerclient.Job{
		ID:        id,
		Source:    workerclient.SourceTelegram,
		Kind:      workerclient.KindLink,
		Link:      ptr(link),
		Note:      ptr(note),
		CreatedAt: 1795113823,
		Blobs:     []workerclient.Blob{},
	}
}

func noteJob(id, note string) workerclient.Job {
	return workerclient.Job{
		ID:        id,
		Source:    workerclient.SourceShortcut,
		Kind:      workerclient.KindNote,
		Note:      ptr(note),
		CreatedAt: 1795113824,
		Blobs:     []workerclient.Blob{},
	}
}

func TestIngestMakesTheItemDurableBeforeReportingDone(t *testing.T) {
	s := openStore(t)
	f := &fakeWorker{batches: [][]workerclient.Job{{linkJob("job-1", "https://Example.com/a?utm_source=x#frag", "read this")}}}

	// The assertion lives in the hook: at the moment `done` is called, the item
	// must already be readable. That is the crash window plans/02 test 8 names.
	var seen []model.Item
	f.doneHook = func(ctx context.Context, _ string) {
		items, err := s.ListItems(ctx, store.ListOptions{})
		if err != nil {
			t.Errorf("ListItems inside done: %v", err)
			return
		}
		seen = items
	}

	ing := NewIngestor(f, s, nil)
	stats, err := ing.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if want := (Stats{Claimed: 1, Ingested: 1}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if len(seen) != 1 {
		t.Fatalf("the item was not readable when done was called: %d items", len(seen))
	}

	item := seen[0]
	if item.URL == nil || *item.URL != "https://example.com/a" {
		t.Errorf("url = %v, want the canonical URL", item.URL)
	}
	if item.URLOriginal != "https://Example.com/a?utm_source=x#frag" {
		t.Errorf("url_original = %q, want what was saved", item.URLOriginal)
	}
	if item.Title != "https://example.com/a" {
		t.Errorf("title = %q, want the URL fallback until enrichment", item.Title)
	}
	if item.Note != "read this" {
		t.Errorf("note = %q", item.Note)
	}
	if item.Kind != model.KindArticle {
		t.Errorf("kind = %q, want article", item.Kind)
	}
	if item.Source != model.SourceTelegram {
		t.Errorf("source = %q, want telegram", item.Source)
	}
	if want := time.Unix(1795113823, 0).UTC(); !item.CreatedAt.Equal(want) {
		t.Errorf("created_at = %v, want the Worker's save time %v", item.CreatedAt, want)
	}

	if got := f.done; len(got) != 1 || got[0] != "job-1" {
		t.Errorf("done calls = %v, want [job-1]", got)
	}
	if len(f.failed) != 0 {
		t.Errorf("failed calls = %v, want none", f.failed)
	}
}

func TestIngestMergesAReSavedURL(t *testing.T) {
	s := openStore(t)
	f := &fakeWorker{batches: [][]workerclient.Job{{
		linkJob("job-1", "https://example.com/a", "one"),
		linkJob("job-2", "https://EXAMPLE.com/a?fbclid=1", "two"),
	}}}

	ing := NewIngestor(f, s, nil)
	stats, err := ing.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if want := (Stats{Claimed: 2, Ingested: 2, Merged: 1}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}

	items, err := s.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("%d items, want one merged item", len(items))
	}
	if !strings.Contains(items[0].Note, "one") || !strings.Contains(items[0].Note, "two") {
		t.Errorf("merged note = %q, want both saves", items[0].Note)
	}
}

func TestIngestReportsAFailedJobAndKeepsDraining(t *testing.T) {
	s := openStore(t)
	bad := linkJob("job-bad", "ftp://example.com/x", "")
	good := noteJob("job-good", "a thought")
	f := &fakeWorker{batches: [][]workerclient.Job{{bad, good}}}

	stats, err := NewIngestor(f, s, nil).Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if want := (Stats{Claimed: 2, Ingested: 1, Failed: 1}); stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
	if reason, ok := f.failed["job-bad"]; !ok || !strings.Contains(reason, "scheme") {
		t.Errorf("failed reason = %q, want the canonicaliser's complaint", reason)
	}
	if _, ok := f.failed["job-good"]; ok {
		t.Error("the good job was reported failed")
	}
	if got := f.done; len(got) != 1 || got[0] != "job-good" {
		t.Errorf("done calls = %v, want only the good job", got)
	}
}

// The save is durable before `done` runs, so a failed `done` must retry the
// call and never rewrite the item (plans/02 test 8).
func TestDoneIsRetriedWithoutReprocessingTheItem(t *testing.T) {
	s := openStore(t)
	f := &fakeWorker{
		batches:  [][]workerclient.Job{{linkJob("job-1", "https://example.com/a", "once")}},
		doneErrs: []error{errors.New("network down"), nil},
	}
	ing := NewIngestor(f, s, nil)
	ing.doneBase = time.Millisecond
	ing.doneAttempts = 3

	stats, err := ing.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Ingested != 1 {
		t.Errorf("stats = %+v, want one ingested", stats)
	}
	if f.doneTries != 2 || len(f.done) != 1 {
		t.Errorf("done tries = %d, successes = %v, want 2 and one success", f.doneTries, f.done)
	}
	items, err := s.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 || strings.Count(items[0].Note, "once") != 1 {
		t.Errorf("items = %d; want one item with one copy of the note", len(items))
	}
}

// A `done` that keeps failing leaves the job claimed. The next claim finds the
// job id already recorded and only retries `done`.
func TestAFailedDoneIsRetriedOnTheNextClaim(t *testing.T) {
	s := openStore(t)
	job := linkJob("job-1", "https://example.com/a", "once")
	f := &fakeWorker{
		batches:  [][]workerclient.Job{{job}, {job}},
		doneErrs: []error{errors.New("down"), errors.New("down"), errors.New("down"), nil},
	}
	ing := NewIngestor(f, s, nil)
	ing.doneBase = time.Millisecond
	ing.doneAttempts = 2

	// First drain: both done attempts fail. The job counts as failed for this
	// pass, but the batch keeps going and the item is already durable.
	stats, err := ing.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Ingested != 1 || stats.Failed != 1 {
		t.Errorf("first drain = %+v, want 1 ingested and 1 failed", stats)
	}

	// Second drain: the same job is claimed again. It is already recorded, so
	// nothing is rewritten and the third done attempt succeeds.
	stats, err = ing.Drain(context.Background())
	if err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if stats.Already != 1 {
		t.Errorf("second drain = %+v, want already=1", stats)
	}
	items, err := s.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 || strings.Count(items[0].Note, "once") != 1 {
		t.Errorf("items = %d, note = %q; the re-claim duplicated the save", len(items), items[0].Note)
	}
}

// plans/02 test 9 — kill the loop after the item commits but before `done`
// reaches the Worker, then start again. The job is re-claimed and finishes
// without a second item or a second copy of the note.
func TestACrashBetweenTheItemAndDoneReplaysNothing(t *testing.T) {
	s := openStore(t)
	job := linkJob("job-1", "https://example.com/a", "once")
	f := &fakeWorker{batches: [][]workerclient.Job{{job}, {job}}}

	ctx, cancel := context.WithCancel(context.Background())
	f.doneHook = func(context.Context, string) { cancel() }
	ing := NewIngestor(f, s, nil)

	first, err := ing.Drain(ctx)
	if err == nil && first.Failed == 0 {
		t.Fatalf("first drain = %+v, want the interrupted done reported", first)
	}
	if n := len(f.done); n != 0 {
		t.Fatalf("done succeeded %d times despite the crash", n)
	}

	// Restart: the job is claimed again, found in processed_jobs, and only the
	// done call is retried.
	f.doneHook = nil
	second, err := ing.Drain(context.Background())
	if err != nil {
		t.Fatalf("second Drain: %v", err)
	}
	if second.Already != 1 {
		t.Errorf("second drain = %+v, want already=1", second)
	}
	if got := f.done; len(got) != 1 || got[0] != "job-1" {
		t.Errorf("done calls = %v, want the re-claimed job finished once", got)
	}
	items, err := s.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 || strings.Count(items[0].Note, "once") != 1 {
		t.Errorf("items = %d; the crash duplicated the save", len(items))
	}
}

func TestIngestWritesANoteJobAsANote(t *testing.T) {
	s := openStore(t)
	f := &fakeWorker{batches: [][]workerclient.Job{{noteJob("job-1", "a thought")}}}
	if _, err := NewIngestor(f, s, nil).Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	items, err := s.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("%d items, want 1", len(items))
	}
	if items[0].URL != nil {
		t.Errorf("url = %v, want nil for a note", *items[0].URL)
	}
	if items[0].Kind != model.KindNote {
		t.Errorf("kind = %q, want note", items[0].Kind)
	}
	if items[0].Source != model.SourceShortcut {
		t.Errorf("source = %q, want shortcut", items[0].Source)
	}
}

func TestIngestRefusesAKindItCannotProcess(t *testing.T) {
	s := openStore(t)
	upload := workerclient.Job{
		ID: "job-1", Source: workerclient.SourceTelegram, Kind: workerclient.KindUpload,
		CreatedAt: 1, Blobs: []workerclient.Blob{{N: 0, Key: "incoming/x/0", ContentType: "image/png", Bytes: 3}},
	}
	f := &fakeWorker{batches: [][]workerclient.Job{{upload}}}
	if _, err := NewIngestor(f, s, nil).Drain(context.Background()); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if reason := f.failed["job-1"]; !strings.Contains(reason, "cannot be ingested") {
		t.Errorf("failed reason = %q, want a refusal", reason)
	}
	if n := len(f.done); n != 0 {
		t.Errorf("done calls = %d, want none", n)
	}
}

func TestDrainKeepsClaimingUntilTheQueueIsEmpty(t *testing.T) {
	s := openStore(t)
	var jobs []workerclient.Job
	for n := 0; n < 12; n++ {
		jobs = append(jobs, noteJob("job-"+string(rune('a'+n)), "thought"))
	}
	f := &fakeWorker{batches: [][]workerclient.Job{jobs[:10], jobs[10:]}}

	stats, err := NewIngestor(f, s, nil).Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if stats.Claimed != 12 || stats.Ingested != 12 {
		t.Errorf("stats = %+v, want 12 claimed and ingested", stats)
	}
	if calls := f.claimCount(); calls != 2 {
		t.Errorf("claim calls = %d, want 2 (a short batch ends the drain)", calls)
	}
	if got := f.kinds[0]; len(got) != 2 || got[0] != workerclient.KindLink || got[1] != workerclient.KindNote {
		t.Errorf("claim kinds = %v, want link,note", got)
	}
	if f.limits[0] != claimBatch {
		t.Errorf("claim limit = %d, want %d", f.limits[0], claimBatch)
	}
}

func TestIngestAllIsIdempotentForReplay(t *testing.T) {
	s := openStore(t)
	jobs := []workerclient.Job{
		linkJob("job-1", "https://example.com/a", "one"),
		linkJob("job-2", "https://example.com/b", "two"),
	}
	f := &fakeWorker{}
	ing := NewIngestor(f, s, nil)

	first, err := ing.IngestAll(context.Background(), jobs)
	if err != nil {
		t.Fatalf("IngestAll: %v", err)
	}
	if first.Ingested != 2 {
		t.Errorf("first replay = %+v, want 2 ingested", first)
	}
	second, err := ing.IngestAll(context.Background(), jobs)
	if err != nil {
		t.Fatalf("second IngestAll: %v", err)
	}
	if second.Already != 2 {
		t.Errorf("second replay = %+v, want both already recorded", second)
	}
	items, err := s.ListItems(context.Background(), store.ListOptions{})
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("%d items after replaying twice, want 2", len(items))
	}
}
