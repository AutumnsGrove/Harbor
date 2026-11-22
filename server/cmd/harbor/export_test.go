package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// The export command is the invariant phase 12 is built around, so it is tested
// through the command a human actually runs, not only through the package.

func exportEnv(t *testing.T, dataDir string) func(string) string {
	t.Helper()
	return func(name string) string {
		switch name {
		case build.EnvDataDir:
			return dataDir
		case build.EnvDeployment:
			return "dev"
		case build.EnvDevNoAuth:
			return "1"
		default:
			return ""
		}
	}
}

func TestExportCommandWritesALibrary(t *testing.T) {
	dataDir := t.TempDir()
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	url := "https://kitchen.example/pancakes"
	item, err := library.CreateItem(context.Background(), store.ItemInput{
		ID: "export-a", URL: &url, Title: "Sourdough pancakes", Kind: model.KindRecipe,
		Summary: "Thin pancakes from starter discard.",
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := library.SetItemTags(context.Background(), item.ID, []string{"baking"}, model.TagSourceAI); err != nil {
		t.Fatal(err)
	}
	// Media is covered at the package level, where a library can be built
	// without reaching into the schema: nothing above store knows about SQL.
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "harbor-export")
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"export", "--out", out},
		exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor export: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "exported the library into") {
		t.Errorf("stdout = %q", stdout.String())
	}

	raw, err := os.ReadFile(filepath.Join(out, "library.json"))
	if err != nil {
		t.Fatalf("library.json: %v\n%s", err, stdout.String())
	}
	var parsed struct {
		Counts struct {
			Items int `json:"items"`
			Media int `json:"media"`
		} `json:"counts"`
		Items []struct {
			ID   string   `json:"id"`
			Tags []string `json:"tags"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("library.json: %v", err)
	}
	if parsed.Counts.Items != 1 || len(parsed.Items) != 1 || parsed.Items[0].ID != "export-a" {
		t.Errorf("library.json = %+v", parsed)
	}
	if len(parsed.Items[0].Tags) != 1 || parsed.Items[0].Tags[0] != "baking" {
		t.Errorf("tags = %v", parsed.Items[0].Tags)
	}
	if _, err := os.Stat(filepath.Join(out, "manifest.json")); err != nil {
		t.Errorf("manifest.json: %v", err)
	}
}

func TestExportCommandRefusesToGuessADestination(t *testing.T) {
	var stdout bytes.Buffer
	err := run(context.Background(), []string{"export"}, exportEnv(t, t.TempDir()), &stdout)
	if err == nil || !strings.Contains(err.Error(), "--out") {
		t.Fatalf("export with no --out = %v", err)
	}
}

func TestExportCommandRefusesAFormatItDoesNotWrite(t *testing.T) {
	var stdout bytes.Buffer
	dir := filepath.Join(t.TempDir(), "export")
	err := run(context.Background(), []string{"export", "--out", dir, "--format", "pdf"},
		exportEnv(t, t.TempDir()), &stdout)
	if err == nil || !strings.Contains(err.Error(), "pdf") {
		t.Fatalf("export --format pdf = %v, want a refusal naming it", err)
	}
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) > 0 {
		t.Errorf("a refused format still wrote %d files", len(entries))
	}
}

func TestExportCommandWritesTheReadableFormats(t *testing.T) {
	dataDir := t.TempDir()
	library, err := store.Open(dataDir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	url := "https://kitchen.example/pancakes"
	item, err := library.CreateItem(context.Background(), store.ItemInput{
		ID: "export-a", URL: &url, Title: "Sourdough pancakes", Kind: model.KindRecipe,
	})
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	if _, err := library.SetItemTags(context.Background(), item.ID, []string{"baking"}, model.TagSourceAI); err != nil {
		t.Fatal(err)
	}
	if err := library.Close(); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "harbor-export")
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"export", "--out", out}, exportEnv(t, dataDir), &stdout); err != nil {
		t.Fatalf("harbor export: %v\n%s", err, stdout.String())
	}
	for _, name := range []string{"library.json", "manifest.json", "bookmarks.html",
		filepath.Join("markdown", "0001-sourdough-pancakes.md")} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if !strings.Contains(stdout.String(), "json, markdown, netscape") {
		t.Errorf("stdout does not say what was written: %q", stdout.String())
	}
}
