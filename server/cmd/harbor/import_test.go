package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/importer"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// The CLI is the real interface for a big file (plans/05), so it is tested
// through the command a human runs, including the dry run that has to be run
// against the real export before anything is committed.

const importCSV = `id,title,note,excerpt,url,folder,tags,created,cover,highlights,favorite
1,Sourdough,feed it daily,,https://example.com/sourdough,Recipes,"baking, bread",2020-03-04T05:06:07.890Z,,Highlight:one,true
2,Second,,,https://example.com/second,Articles,,2021-01-02T03:04:05Z,https://img.example.com/2.jpg,,false
`

// mustLibrary reads the whole library, which is how a test asserts what a run
// left behind.
func mustLibrary(t *testing.T, library *store.Store) model.Library {
	t.Helper()
	exported, err := library.ExportLibrary(context.Background())
	if err != nil {
		t.Fatalf("ExportLibrary: %v", err)
	}
	return exported
}

func writeImportFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func TestImportCommandDryRunThenReal(t *testing.T) {
	dataDir := t.TempDir()
	path := writeImportFile(t, "export.csv", importCSV)

	var dry bytes.Buffer
	if err := run(context.Background(), []string{"import", path, "--dry-run"}, exportEnv(t, dataDir), &dry); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(dry.String(), "would import 2 of 2 rows") {
		t.Errorf("dry run printed %q, want the summary", dry.String())
	}
	if !strings.Contains(dry.String(), "2 Spaces would be created") {
		t.Errorf("dry run printed %q, want the Spaces to create", dry.String())
	}
	if !strings.Contains(dry.String(), "nothing was written") {
		t.Errorf("dry run should say it wrote nothing: %q", dry.String())
	}

	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if n := len(mustLibrary(t, library).Items); n != 0 {
		t.Fatalf("the dry run wrote %d items", n)
	}
	_ = library.Close()

	var real bytes.Buffer
	if err := run(context.Background(), []string{"import", path}, exportEnv(t, dataDir), &real); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(real.String(), "imported 2 of 2 rows") {
		t.Errorf("import printed %q", real.String())
	}

	library, err = store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()
	items := mustLibrary(t, library).Items
	if len(items) != 2 {
		t.Fatalf("%d items after the import, want 2", len(items))
	}
}

// The format is chosen from the contents, not the name. The HTML here has no
// .html suffix and the CSV below is called bookmarks.html, so an extension-based
// implementation fails both halves.
func TestImportCommandDetectsHTML(t *testing.T) {
	dataDir := t.TempDir()
	path := writeImportFile(t, "3cad2d1c-677d-4697-a100-69c64d3e72f4", `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<DL><p>
    <DT><A HREF="https://example.com/a" ADD_DATE="1700000000">A &amp; B</A>
</DL><p>
`)

	var out bytes.Buffer
	if err := run(context.Background(), []string{"import", path}, exportEnv(t, dataDir), &out); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(out.String(), "imported 1 of 1 rows") {
		t.Errorf("printed %q", out.String())
	}

	// The mirror image: a Raindrop CSV that happens to be called bookmarks.html.
	csvPath := writeImportFile(t, "bookmarks.html", importCSV)
	out.Reset()
	if err := run(context.Background(), []string{"import", csvPath, "--dry-run"}, exportEnv(t, t.TempDir()), &out); err != nil {
		t.Fatalf("CSV named bookmarks.html: %v", err)
	}
	if !strings.Contains(out.String(), "would import 2 of 2 rows") {
		t.Errorf("a CSV named bookmarks.html printed %q", out.String())
	}
}

// The JSON report is the shape Settings → Import will read, so the CLI has to be
// able to write one.
func TestImportCommandWritesAJSONReport(t *testing.T) {
	dataDir := t.TempDir()
	path := writeImportFile(t, "export.csv", importCSV)
	reportPath := filepath.Join(t.TempDir(), "report.json")

	var out bytes.Buffer
	if err := run(context.Background(), []string{"import", path, "--dry-run", "--report", reportPath},
		exportEnv(t, dataDir), &out); err != nil {
		t.Fatalf("import: %v", err)
	}

	encoded, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("reading the report: %v", err)
	}
	var report importer.Report
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatalf("the report is not JSON: %v\n%s", err, encoded)
	}
	if report.Total != 2 || report.Imported != 2 || report.Favourites != 1 || report.Highlights != 1 {
		t.Errorf("report = %+v", report)
	}
}

func TestImportCommandRejectsBadUsage(t *testing.T) {
	dataDir := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no file", []string{"import"}, "exactly one file"},
		{"two files", []string{"import", "a.csv", "b.csv"}, "exactly one file"},
		{"unknown flag", []string{"import", "--nope", "a.csv"}, "not defined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := run(context.Background(), tc.args, exportEnv(t, dataDir), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

func TestImportCommandRejectsAFileThatIsNotAnExport(t *testing.T) {
	dataDir := t.TempDir()
	path := writeImportFile(t, "notes.txt", "just some text\n")
	err := run(context.Background(), []string{"import", path}, exportEnv(t, dataDir), &bytes.Buffer{})
	if err == nil {
		t.Fatal("a text file was accepted as an import")
	}
}

func TestImportCommandUsesTheDataDirectory(t *testing.T) {
	dir := t.TempDir()
	path := writeImportFile(t, "export.csv", importCSV)
	if err := run(context.Background(), []string{"import", path}, exportEnv(t, dir), &bytes.Buffer{}); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, store.DBFile)); err != nil {
		t.Errorf("the import did not write to the configured data directory: %v", err)
	}
}

// plans/05 test 6 at the command level: a cancelled run of the same file
// resumes from its checkpoint, so the rows it already applied are not re-read.
func TestImportCommandResumesACancelledRun(t *testing.T) {
	dataDir := t.TempDir()
	path := writeImportFile(t, "export.csv", importCSV)

	// Seed the state a Ctrl-C leaves behind: the first row applied, and a
	// cancelled run whose checkpoint says so.
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	first := "https://example.com/sourdough"
	if _, err := library.CreateItem(context.Background(), store.ItemInput{
		ID: "row-1", URL: &first, Title: "Sourdough", Note: "feed it daily", Source: model.SourceImport,
	}); err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	sum := sha256File(t, path)
	run, _, err := library.StartImportRun(context.Background(), sum, "{}")
	if err != nil {
		t.Fatalf("StartImportRun: %v", err)
	}
	if err := library.FinishImportRun(context.Background(), run.ID, store.ImportCancelled, 1, "{}"); err != nil {
		t.Fatalf("FinishImportRun: %v", err)
	}
	_ = library.Close()

	var out bytes.Buffer
	if err := run2(t, []string{"import", path}, exportEnv(t, dataDir), &out); err != nil {
		t.Fatalf("import: %v", err)
	}
	if !strings.Contains(out.String(), "resuming the cancelled run of this file from record 1") {
		t.Errorf("printed %q, want the resume notice", out.String())
	}
	if !strings.Contains(out.String(), "imported 1 of 1 rows") {
		t.Errorf("printed %q, want only the row after the checkpoint", out.String())
	}

	library, err = store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()
	finished, err := library.ImportRunByID(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("ImportRunByID: %v", err)
	}
	if finished.State != store.ImportDone || finished.Checkpoint != 2 {
		t.Errorf("run = %+v, want done at record 2", finished)
	}
	if n := len(mustLibrary(t, library).Items); n != 2 {
		t.Errorf("%d items after the resume, want 2", n)
	}
}

// A finished run is not resumed: importing the same file again is a fresh run
// whose rows all merge.
func TestImportCommandStartsFreshAfterAFinishedRun(t *testing.T) {
	dataDir := t.TempDir()
	path := writeImportFile(t, "export.csv", importCSV)

	var first bytes.Buffer
	if err := run2(t, []string{"import", path, "--no-enrich"}, exportEnv(t, dataDir), &first); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if strings.Contains(first.String(), "resuming") {
		t.Fatalf("the first import claimed to resume: %q", first.String())
	}

	var second bytes.Buffer
	if err := run2(t, []string{"import", path}, exportEnv(t, dataDir), &second); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if strings.Contains(second.String(), "resuming") {
		t.Errorf("a finished run was resumed: %q", second.String())
	}
	if !strings.Contains(second.String(), "imported 0 of 2 rows") || !strings.Contains(second.String(), "2 merged") {
		t.Errorf("second import printed %q, want everything merged", second.String())
	}
}

// The dry run leaves no import_run behind, so a preview cannot make the real
// run resume from a checkpoint it never wrote.
func TestImportCommandDryRunLeavesNoRun(t *testing.T) {
	dataDir := t.TempDir()
	path := writeImportFile(t, "export.csv", importCSV)
	if err := run2(t, []string{"import", path, "--dry-run"}, exportEnv(t, dataDir), &bytes.Buffer{}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()
	if _, err := library.LatestImportRun(context.Background()); err == nil {
		t.Error("the dry run left an import run behind")
	}
}

// run2 is run with the test's context, kept out of the way of the other tests.
func run2(t *testing.T, args []string, env func(string) string, stdout *bytes.Buffer) error {
	t.Helper()
	return run(context.Background(), args, env, stdout)
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// A cancelled context is a controlled stop: the run is closed as cancelled at
// the row it reached, on a context that survives the interrupt, and the command
// does not report a failure. The writes have to ignore the dead ctx — that is
// what this asserts, since a real Ctrl-C cannot be timed deterministically.
func TestFinishImportRunSurvivesCancellation(t *testing.T) {
	dataDir := t.TempDir()
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer library.Close()

	ctx := context.Background()
	run, _, err := library.StartImportRun(ctx, "sha-a", "{}")
	if err != nil {
		t.Fatalf("StartImportRun: %v", err)
	}

	dead, cancel := context.WithCancel(ctx)
	cancel()
	if err := finishImportRun(dead, library, run.ID, 420, importer.Report{Total: 420, Imported: 420}, true); err != nil {
		t.Fatalf("finishImportRun on a cancelled context: %v", err)
	}

	finished, err := library.ImportRunByID(ctx, run.ID)
	if err != nil {
		t.Fatalf("ImportRunByID: %v", err)
	}
	if finished.State != store.ImportCancelled {
		t.Errorf("state = %q, want cancelled", finished.State)
	}
	if finished.Checkpoint != 420 {
		t.Errorf("checkpoint = %d, want 420: the operator was told it stopped there", finished.Checkpoint)
	}
	if finished.FinishedAt == nil {
		t.Error("finished_at is nil; the run was left open")
	}
	if !strings.Contains(finished.CountsJSON, `"imported":420`) {
		t.Errorf("counts = %s, want the report's numbers", finished.CountsJSON)
	}
}
