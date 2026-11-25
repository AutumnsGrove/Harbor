package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Settings → Import is the importer with a door: the run, its checkpoint and
// its resume rules are the CLI's (plans/05), and what is tested here is the
// layer that starts it without a terminal — the background run, the poll, the
// cancel, and the refusals.

const importBody = `id,title,note,excerpt,url,folder,tags,created,cover,highlights,favorite
1,Sourdough,feed it daily,,https://example.com/sourdough,Recipes,"baking, bread",2020-03-04T05:06:07.890Z,,Highlight:one,true
2,Second,,,https://example.com/second,Articles,,2021-01-02T03:04:05Z,https://img.example.com/2.jpg,,false
3,Third,,,https://example.com/third,,,2022-05-06T07:08:09Z,,,false
`

func importHandler(t *testing.T) (http.Handler, *store.Store) {
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

// upload posts a file the way the page does: the body is the file.
func upload(t *testing.T, h http.Handler, body, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/import"+query, strings.NewReader(body))
	req.Host = "localhost"
	req.Header.Set(identityHeader, "autumn@grove.place")
	req.Header.Set("content-type", "text/csv")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func importStatus(t *testing.T, h http.Handler) importStatusResponse {
	t.Helper()
	rec := authed(h, http.MethodGet, "/api/import/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/import/status = %d: %s", rec.Code, rec.Body.String())
	}
	var body importStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status body %q is not JSON: %v", rec.Body.String(), err)
	}
	return body
}

// waitForImport polls like the page does, and fails rather than hangs.
func waitForImport(t *testing.T, h http.Handler) importStatusResponse {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := importStatus(t, h)
		if !status.Running && status.Run != nil && status.Run.State != store.ImportRunning {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the import did not finish in 10 seconds")
	return importStatusResponse{}
}

func itemCount(t *testing.T, library *store.Store) int {
	t.Helper()
	counts, err := library.Counts(context.Background(), time.Now().UTC().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	return counts.Items
}

// The dry run is the one people run against the real export first: it reports
// and writes nothing, no import run included.
func TestImportDryRunReportsAndWritesNothing(t *testing.T) {
	h, library := importHandler(t)

	rec := upload(t, h, importBody, "?dryRun=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/import?dryRun = %d: %s", rec.Code, rec.Body.String())
	}
	var body importDryRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if !body.DryRun {
		t.Error("dryRun is false; a client cannot tell the answer from a real run")
	}
	if body.Report.Total != 3 || body.Report.Imported != 3 || body.Report.Highlights != 1 {
		t.Errorf("report = %+v, want three rows, three imports and one highlight", body.Report)
	}
	if got, want := body.BytesTotal, int64(len(importBody)); got != want {
		t.Errorf("bytesTotal = %d, want %d", got, want)
	}

	if n := itemCount(t, library); n != 0 {
		t.Errorf("the dry run wrote %d items", n)
	}
	if _, err := library.LatestImportRun(context.Background()); err == nil {
		t.Error("the dry run left an import run behind")
	}
}

// The real upload answers immediately and the run continues without the
// request: the poll is where the counts and the finished state come from.
func TestImportRunsInTheBackgroundAndThePollReportsIt(t *testing.T) {
	h, library := importHandler(t)

	rec := upload(t, h, importBody, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/import = %d: %s", rec.Code, rec.Body.String())
	}

	status := waitForImport(t, h)
	if status.Run == nil {
		t.Fatal("the finished run is not reported")
	}
	if status.Run.State != store.ImportDone || status.Run.FinishedAt == nil {
		t.Errorf("run = %+v, want done with a finish time", status.Run)
	}
	if status.Run.Checkpoint != 3 {
		t.Errorf("checkpoint = %d, want 3", status.Run.Checkpoint)
	}
	if status.Run.Report == nil || status.Run.Report.Imported != 3 {
		t.Errorf("report = %+v, want three imported", status.Run.Report)
	}
	if n := itemCount(t, library); n != 3 {
		t.Errorf("%d items in the library, want 3", n)
	}

	// The default is the documented one: items land usable, and enrichment is a
	// separate, watched run.
	first := "https://example.com/sourdough"
	item, err := library.GetItemByURL(context.Background(), first)
	if err != nil {
		t.Fatalf("GetItemByURL: %v", err)
	}
	if item.State != model.StateReady {
		t.Errorf("state = %q, want ready: the bulk default does not queue for the model", item.State)
	}
}

// enrich=1 is the watched run: the items enter the queue instead of landing
// ready, behind anything saved interactively (plans/05 test 10).
func TestImportCanQueueForEnrichment(t *testing.T) {
	h, library := importHandler(t)

	rec := upload(t, h, importBody, "?enrich=1")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/import?enrich = %d: %s", rec.Code, rec.Body.String())
	}
	waitForImport(t, h)

	item, err := library.GetItemByURL(context.Background(), "https://example.com/second")
	if err != nil {
		t.Fatalf("GetItemByURL: %v", err)
	}
	if item.State != model.StateQueued {
		t.Errorf("state = %q, want queued when enrichment was asked for", item.State)
	}
}

// The second upload of the same file merges: idempotence is the property the
// whole phase leans on, and it has to hold through this door too.
func TestImportTwiceMerges(t *testing.T) {
	h, library := importHandler(t)

	for range 2 {
		if rec := upload(t, h, importBody, ""); rec.Code != http.StatusAccepted {
			t.Fatalf("POST /api/import = %d: %s", rec.Code, rec.Body.String())
		}
		waitForImport(t, h)
	}

	if n := itemCount(t, library); n != 3 {
		t.Errorf("%d items after two imports, want 3", n)
	}
	status := importStatus(t, h)
	if status.Run == nil || status.Run.Report == nil {
		t.Fatal("the second run is not reported")
	}
	if status.Run.State != store.ImportDone {
		t.Errorf("state = %q, want a fresh done run: a finished run is not resumed", status.Run.State)
	}
	if status.Run.Report.Imported != 0 || status.Run.Report.Merged != 3 {
		t.Errorf("report = %+v, want everything merged", status.Run.Report)
	}
}

// A file that is not an export is refused before anything is written, with the
// reader's own sentence: the page shows it, and it says what was expected.
func TestImportRefusesAFileThatIsNotAnExport(t *testing.T) {
	h, library := importHandler(t)

	rec := upload(t, h, "this is a shopping list\n", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/import = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if message := rec.Body.String(); !strings.Contains(message, "Raindrop") {
		t.Errorf("error %q does not say what was expected", message)
	}
	if n := itemCount(t, library); n != 0 {
		t.Errorf("the refused upload wrote %d items", n)
	}
	if importStatus(t, h).Running {
		t.Error("the refused upload left the app busy")
	}
}

func TestImportRefusesAnEmptyUpload(t *testing.T) {
	h, _ := importHandler(t)
	if rec := upload(t, h, "", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/import with no body = %d, want 400", rec.Code)
	}
}

// Import writes the library, so it is behind the identity lock like every other
// write; a headerless request from the tailnet must not reach it.
func TestImportRequiresIdentity(t *testing.T) {
	h, _ := importHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/import", strings.NewReader(importBody))
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/import without identity = %d, want 403", rec.Code)
	}
}

func TestImportCancelWithNothingRunning(t *testing.T) {
	h, _ := importHandler(t)
	if rec := authed(h, http.MethodPost, "/api/import/cancel"); rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/import/cancel = %d, want 409", rec.Code)
	}
}

// bigImport is a file long enough that a cancel lands in the middle of it: a
// couple of hundred milliseconds of row writes.
func bigImport(rows int) string {
	var body strings.Builder
	body.WriteString("id,title,note,excerpt,url,folder,tags,created,cover,highlights,favorite\n")
	for i := range rows {
		body.WriteString(strconv.Itoa(i))
		body.WriteString(",Title,,,https://example.com/")
		body.WriteString(strconv.Itoa(i))
		body.WriteString(",,,2020-01-02T03:04:05Z,,,false\n")
	}
	return body.String()
}

// waitForRun is the first poll that sees the run's own row, which is what makes
// a cancel reach the importer: before it, the run is only a slot.
func waitForRun(t *testing.T, h http.Handler) importStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status := importStatus(t, h)
		if status.Run != nil {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the run never appeared in import_runs")
	return importStatusResponse{}
}

// Cancel stops the run at the next record and closes it as cancelled, which is
// what makes the same file resumable.
func TestImportCancelStopsTheRun(t *testing.T) {
	h, library := importHandler(t)

	if rec := upload(t, h, bigImport(2_000), ""); rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/import = %d: %s", rec.Code, rec.Body.String())
	}
	waitForRun(t, h)
	// Long enough for rows to land, short enough that the file is not finished.
	time.Sleep(25 * time.Millisecond)

	if rec := authed(h, http.MethodPost, "/api/import/cancel"); rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/import/cancel = %d: %s", rec.Code, rec.Body.String())
	}

	status := waitForImport(t, h)
	if status.Run == nil {
		t.Fatal("the run is not reported")
	}
	if status.Run.State != store.ImportCancelled {
		t.Fatalf("state = %q, want cancelled: the file had not been read to the end", status.Run.State)
	}
	if n := itemCount(t, library); n == 0 {
		t.Error("a cancelled run wrote nothing at all; that is a failure, not a cancel")
	}
	if status.Run.Report == nil || status.Run.Report.Total >= 2_000 {
		t.Errorf("report = %+v, want a run that stopped before the end", status.Run.Report)
	}
}

// The cancel can arrive in the gap between the upload answering and the run
// opening its row — the poll says a run is going as soon as the slot is taken.
// It has to be remembered and applied, not lost: a cancel that leaves no run
// behind is indistinguishable from an import that never started.
func TestImportCancelBeforeTheRunRowExistsStillStopsIt(t *testing.T) {
	h, _ := importHandler(t)

	if rec := upload(t, h, bigImport(2_000), ""); rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/import = %d: %s", rec.Code, rec.Body.String())
	}
	// No waiting: this is the race, exercised rather than hoped away.
	rec := authed(h, http.MethodPost, "/api/import/cancel")
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/import/cancel = %d: %s", rec.Code, rec.Body.String())
	}

	status := waitForImport(t, h)
	if status.Run == nil {
		t.Fatal("the cancelled import left no run behind to resume")
	}
	if status.Run.State != store.ImportCancelled {
		t.Errorf("state = %q, want cancelled", status.Run.State)
	}
	if status.Run.Checkpoint != 0 {
		t.Errorf("checkpoint = %d, want 0: a run stopped before its first record", status.Run.Checkpoint)
	}
}

// A run left "running" by a process that died is not a run in progress, and the
// page has to be able to say so: uploading the same file resumes it.
func TestImportStatusReportsAnInterruptedRunAsResumable(t *testing.T) {
	h, library := importHandler(t)

	run, _, err := library.StartImportRun(context.Background(), "sha-interrupted", "{}")
	if err != nil {
		t.Fatalf("StartImportRun: %v", err)
	}

	status := importStatus(t, h)
	if status.Running {
		t.Error("nothing is running, but the poll says it is")
	}
	if !status.Resumable {
		t.Errorf("status = %+v, want resumable: the row says running and no run is in flight", status)
	}
	if status.Run == nil || status.Run.ID != run.ID {
		t.Errorf("run = %+v, want the interrupted one", status.Run)
	}
}

// An interrupted run is resumed by uploading the same file again: it skips the
// records the checkpoint covers instead of re-reading them.
func TestImportResumesAnInterruptedRun(t *testing.T) {
	h, library := importHandler(t)

	sum := sha256Body(importBody)
	run, _, err := library.StartImportRun(context.Background(), sum, "{}")
	if err != nil {
		t.Fatalf("StartImportRun: %v", err)
	}
	if err := library.FinishImportRun(context.Background(), run.ID, store.ImportCancelled, 2, "{}"); err != nil {
		t.Fatalf("FinishImportRun: %v", err)
	}

	if rec := upload(t, h, importBody, ""); rec.Code != http.StatusAccepted {
		t.Fatalf("POST /api/import = %d: %s", rec.Code, rec.Body.String())
	}
	status := waitForImport(t, h)
	if status.Run == nil || status.Run.ID != run.ID {
		t.Fatalf("run = %+v, want the resumed run %s", status.Run, run.ID)
	}
	if status.Run.Checkpoint != 3 {
		t.Errorf("checkpoint = %d, want 3 after the resume finished the file", status.Run.Checkpoint)
	}
	if status.Run.Report == nil || status.Run.Report.Total != 1 {
		t.Errorf("report = %+v, want only the record after the checkpoint", status.Run.Report)
	}
	if n := itemCount(t, library); n != 1 {
		t.Errorf("%d items, want 1: the rows the checkpoint skipped were never written", n)
	}
}

// One import at a time: a second run would share the checkpoint, which is the
// one way to make an import lose a row.
// sha256Body is the run key the handler computes from the upload.
func sha256Body(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func TestImportManagerRefusesASecondRun(t *testing.T) {
	m := newImportManager(slog.New(slog.DiscardHandler))
	cancelled := false
	first := &importJob{cancel: func() { cancelled = true }}
	if !m.begin(first) {
		t.Fatal("the first run was refused")
	}
	if m.begin(&importJob{cancel: func() {}}) {
		t.Error("a second run was accepted while one was going")
	}
	// The cancel arrives before the run has armed: remembered, not applied.
	if !m.cancel() || cancelled {
		t.Error("cancel fired before the run opened its row")
	}
	if m.active.stop == false {
		t.Error("the early cancel was not remembered")
	}
	m.armed(first)
	if !cancelled {
		t.Error("arming a cancelled run did not stop it")
	}
	m.done(first)
	if m.running() {
		t.Error("the manager still reports a run after it finished")
	}
	if !m.begin(&importJob{cancel: func() {}}) {
		t.Error("the slot was not released")
	}
}

// A server with no library (a build without a database) answers 503 rather than
// panicking on a nil store.
func TestImportWithoutALibrary(t *testing.T) {
	cfg := testConfig(t)
	h := New(cfg, slog.New(slog.DiscardHandler), testAssets()).Handler()

	if rec := upload(t, h, importBody, ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST /api/import = %d, want 503", rec.Code)
	}
	if rec := authed(h, http.MethodGet, "/api/import/status"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /api/import/status = %d, want 503", rec.Code)
	}
}
