package main

import (
	"bytes"
	"context"
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

// A bookmarks.html is detected by its contents, not its name, and imports
// through the same command.
func TestImportCommandDetectsHTML(t *testing.T) {
	dataDir := t.TempDir()
	path := writeImportFile(t, "bookmarks.html", `<!DOCTYPE NETSCAPE-Bookmark-file-1>
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
