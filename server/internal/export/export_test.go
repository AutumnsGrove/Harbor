package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// plans/12a, "Tests first" 1 and 3: the export is a contract, so it is diffed
// against committed goldens, and its manifest is checked rather than trusted.

var update = flag.Bool("update", false, "rewrite the golden export files")

const (
	testVersion = "r1.abcdef"
	testSchema  = 1
)

var fixedNow = time.Date(2026, 11, 22, 18, 0, 0, 0, time.UTC)

// mediaBytes is the fixture blob; its content address is computed, not invented,
// because the export checks the two agree.
const mediaBytes = "jpegbytes"

func shaOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// fixtureLibrary is a library with one of everything the contract names: a
// normal item, a typed note, an item in "Recently deleted", tags, a Space, a
// highlight, and a media row.
func fixtureLibrary() model.Library {
	url := "https://kitchen.example/pancakes"
	slug := "read-later"
	width, height := 1200, 900
	mediaHash := shaOf(mediaBytes)
	return model.Library{
		Items: []model.Item{
			{
				ID: "0190-item-a", URL: &url, URLOriginal: "https://kitchen.example/pancakes?utm_source=x",
				Title: "Sourdough pancakes", Note: "Try with the rye starter.",
				Kind: model.KindRecipe, KindSource: model.KindSourceAI,
				Summary:     "Thin pancakes from starter discard. Good for a slow morning.",
				ContentText: "Whisk the starter, egg and milk.\nRest, then cook.",
				ContentMD:   "Whisk the starter, egg and milk.\n\nRest, then cook.",
				DetailsJSON: `{"recipe":{"ingredients":["starter","egg"]}}`,
				OGJSON:      `{"site":"Kitchen"}`,
				Source:      model.SourceApp,
				CreatedAt:   fixedNow.Add(-96 * time.Hour).Truncate(time.Second),
				SavedAt:     fixedNow.Add(-72 * time.Hour).Truncate(time.Second),
				UpdatedAt:   fixedNow.Add(-48 * time.Hour).Truncate(time.Second),
			},
			{
				ID: "0190-item-b", Title: "A thought", Note: "Save the gesture, not the library.",
				Kind: model.KindNote, KindSource: model.KindSourceDefault,
				Summary:   "A note about the shape of the product.",
				Source:    model.SourceApp,
				CreatedAt: fixedNow.Add(-24 * time.Hour).Truncate(time.Second),
				SavedAt:   fixedNow.Add(-24 * time.Hour).Truncate(time.Second),
				UpdatedAt: fixedNow.Add(-24 * time.Hour).Truncate(time.Second),
			},
			{
				ID: "0190-item-c", URL: &url, Title: "Deleted but still mine",
				Kind: model.KindArticle, KindSource: model.KindSourceAI,
				CreatedAt: fixedNow.Add(-120 * time.Hour).Truncate(time.Second),
				SavedAt:   fixedNow.Add(-120 * time.Hour).Truncate(time.Second),
				UpdatedAt: fixedNow.Add(-120 * time.Hour).Truncate(time.Second),
				DeletedAt: timePtr(fixedNow.Add(-12 * time.Hour).Truncate(time.Second)),
			},
		},
		Tags: map[string][]string{
			"0190-item-a": {"baking", "breakfast"},
		},
		Spaces: []model.Space{
			{ID: "0190-space", Slug: &slug, Name: "Read later", Kind: model.SpaceManual,
				RuleJSON: `{}`, CreatedAt: fixedNow.Add(-500 * time.Hour).Truncate(time.Second)},
		},
		SpaceItems: map[string][]string{
			"0190-space": {"0190-item-a"},
		},
		Highlights: []model.Highlight{
			{ID: "0190-hl", ItemID: "0190-item-a", Quote: "Rest, then cook.",
				Note: "Fifteen minutes, not five.", Color: model.ColorAmber,
				AnchorJSON: `{"quote":"Rest, then cook.","ordinal":1}`,
				CreatedAt:  fixedNow.Add(-40 * time.Hour).Truncate(time.Second)},
		},
		Media: []model.Media{
			{ID: "0190-media", ItemID: "0190-item-a", Kind: model.MediaImage,
				Filename: "pancakes.jpg", ContentType: "image/jpeg", Bytes: 2048,
				SHA256: mediaHash, Width: &width, Height: &height,
				LocalPath: "aa/" + mediaHash, CreatedAt: fixedNow.Add(-71 * time.Hour).Truncate(time.Second)},
		},
	}
}

func timePtr(t time.Time) *time.Time { return &t }

type fakeReader struct {
	library model.Library
	err     error
}

func (f fakeReader) ExportLibrary(context.Context) (model.Library, error) {
	return f.library, f.err
}

func options(dir string) Options {
	return Options{Dir: dir, AppVersion: testVersion, SchemaVersion: testSchema, Now: func() time.Time { return fixedNow }}
}

func TestJSONWritesTheLibraryAndItsManifest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "harbor-export-20261122")
	result, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, options(dir))
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	for _, name := range []string{"library.json", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !result.Has(name) {
			t.Errorf("%s is missing from the result manifest: %+v", name, result.Files)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ExportVersion int    `json:"export_version"`
		AppVersion    string `json:"app_version"`
		SchemaVersion int    `json:"schema_version"`
		ExportedAt    string `json:"exported_at"`
		Counts        struct {
			Items int `json:"items"`
		} `json:"counts"`
		Items []struct {
			ID     string   `json:"id"`
			Kind   string   `json:"kind"`
			Tags   []string `json:"tags"`
			Spaces []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"spaces"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("library.json is not JSON: %v", err)
	}
	if parsed.ExportVersion != ExportVersion || parsed.SchemaVersion != testSchema {
		t.Errorf("versions = %d/%d", parsed.ExportVersion, parsed.SchemaVersion)
	}
	if parsed.ExportedAt != "2026-11-22T18:00:00Z" {
		t.Errorf("exported_at = %q, want the injected clock", parsed.ExportedAt)
	}
	if parsed.Counts.Items != 3 || len(parsed.Items) != 3 {
		t.Fatalf("items = %d/%d, want all three including the deleted one", parsed.Counts.Items, len(parsed.Items))
	}
	first := parsed.Items[0]
	if len(first.Tags) != 2 || first.Tags[0] != "baking" {
		t.Errorf("tags = %v, want the item's canonical tags", first.Tags)
	}
	// Membership carries the id as well as the name: Space names are not unique
	// in the schema, so a restore cannot match on the name alone.
	if len(first.Spaces) != 1 || first.Spaces[0].ID != "0190-space" || first.Spaces[0].Name != "Read later" {
		t.Errorf("spaces = %+v, want the Space's id and name", first.Spaces)
	}
	if first.Kind != "recipe" {
		t.Errorf("kind = %q", first.Kind)
	}
}

func TestJSONIsDeterministic(t *testing.T) {
	// Two exports of an unchanged library must be byte-identical, or a diff is
	// noise and the manifest is worthless.
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	for _, dir := range []string{a, b} {
		if _, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, options(dir)); err != nil {
			t.Fatalf("JSON: %v", err)
		}
	}
	for _, name := range []string{"library.json", "manifest.json"} {
		first, err := os.ReadFile(filepath.Join(a, name))
		if err != nil {
			t.Fatal(err)
		}
		second, err := os.ReadFile(filepath.Join(b, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(second) {
			t.Errorf("%s differs between two exports of the same library", name)
		}
	}
}

func TestTheManifestHashesEveryFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "export")
	result, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, options(dir))
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var manifest struct {
		Files []struct {
			Path   string `json:"path"`
			Bytes  int64  `json:"bytes"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	// The result names every file it wrote; the manifest names all but itself.
	if len(manifest.Files) != len(result.Files)-1 {
		t.Fatalf("manifest lists %d files, result has %d (the manifest is the difference)",
			len(manifest.Files), len(result.Files))
	}
	for _, file := range manifest.Files {
		if file.Path == "manifest.json" {
			t.Errorf("the manifest hashed itself")
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.Path)))
		if err != nil {
			t.Fatalf("%s: %v", file.Path, err)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != file.SHA256 {
			t.Errorf("%s sha256 = %s in the manifest, %s on disk", file.Path, file.SHA256, got)
		}
		if int64(len(body)) != file.Bytes {
			t.Errorf("%s bytes = %d in the manifest, %d on disk", file.Path, file.Bytes, len(body))
		}
	}
}

func TestMediaIsCopiedOnlyWhenAsked(t *testing.T) {
	sha := shaOf(mediaBytes)
	mediaDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mediaDir, "aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "aa", sha), []byte(mediaBytes), 0o644); err != nil {
		t.Fatal(err)
	}

	without := filepath.Join(t.TempDir(), "plain")
	result, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, options(without))
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if result.Has("media/" + sha) {
		t.Error("media was copied without --include-media")
	}

	with := options(filepath.Join(t.TempDir(), "with-media"))
	with.IncludeMedia = true
	with.MediaDir = mediaDir
	withResult, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, with)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !withResult.Has("media/" + sha) {
		t.Fatalf("media was not copied: %+v", withResult.Files)
	}
	copied, err := os.ReadFile(filepath.Join(with.Dir, "media", sha))
	if err != nil || string(copied) != mediaBytes {
		t.Errorf("copied media = %q, %v", copied, err)
	}
}

func TestMissingMediaFailsTheExport(t *testing.T) {
	// A manifest that lists a hash with no bytes is a broken promise, so this is
	// an error, not a warning — and it must leave nothing behind, or the obvious
	// retry would be refused for a directory that only holds the debris.
	dir := filepath.Join(t.TempDir(), "export")
	opts := options(dir)
	opts.IncludeMedia = true
	opts.MediaDir = t.TempDir()
	if _, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, opts); err == nil {
		t.Fatal("an export with a missing media file should fail")
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		t.Errorf("a failed export left %d entries in %s", len(entries), dir)
	}
	// And the retry, once the blob exists, works into the same directory.
	if err := os.MkdirAll(filepath.Join(opts.MediaDir, "aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opts.MediaDir, "aa", shaOf(mediaBytes)), []byte(mediaBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, opts); err != nil {
		t.Fatalf("retrying into the same directory: %v", err)
	}
}

func TestMediaThatDoesNotMatchItsHashFailsTheExport(t *testing.T) {
	// The blob is stored under its content address, so a mismatch means the file
	// is corrupt. Copying it would put corrupt bytes under a name the manifest
	// then vouches for.
	mediaDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mediaDir, "aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Stored under its content address, but the bytes inside are something else.
	if err := os.WriteFile(filepath.Join(mediaDir, "aa", shaOf(mediaBytes)), []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "export")
	opts := options(dir)
	opts.IncludeMedia = true
	opts.MediaDir = mediaDir

	_, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, opts)
	if err == nil {
		t.Fatal("a blob that does not match its content address should fail the export")
	}
	if !strings.Contains(err.Error(), shaOf(mediaBytes)) {
		t.Errorf("error = %v, want it to name the hash", err)
	}
	if entries, readErr := os.ReadDir(dir); readErr == nil && len(entries) > 0 {
		t.Errorf("a failed export left %d entries in %s", len(entries), dir)
	}
}

func TestExportRefusesTheCurrentDirectory(t *testing.T) {
	// `--out .` is a directory the export would have to replace, and replacing
	// the directory a shell is sitting in leaves that shell in a deleted inode.
	// Say so, rather than failing later with a rename error.
	t.Chdir(t.TempDir())
	_, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, options("."))
	if err == nil {
		t.Fatal("exporting into the current directory should be refused")
	}
	if !strings.Contains(err.Error(), "current directory") {
		t.Errorf("error = %v, want it to say why", err)
	}
}

func TestExportRefusesTheCurrentDirectoryThroughASymlink(t *testing.T) {
	// The same directory spelled another way is still the directory the shell is
	// sitting in, so the guard has to compare resolved paths, not strings.
	work := t.TempDir()
	t.Chdir(work)
	link := filepath.Join(t.TempDir(), "work-link")
	if err := os.Symlink(work, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, options(link))
	if err == nil || !strings.Contains(err.Error(), "current directory") {
		t.Fatalf("exporting through a symlink to the current directory = %v, want a refusal", err)
	}
}

func TestExportIntoNamesOfADirectoryThatDoesNotExistYet(t *testing.T) {
	// The documented usage: --out ./harbor-export-YYYYMMDD, relative and new.
	t.Chdir(t.TempDir())
	result, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, options("harbor-export-20261122"))
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.Dir, "library.json")); err != nil {
		t.Errorf("library.json: %v", err)
	}
}

func TestExportRefusesADirectoryWithFilesInIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := JSON(context.Background(), fakeReader{library: fixtureLibrary()}, options(dir)); err == nil {
		t.Fatal("exporting into a directory that already holds files should fail")
	}
	body, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
	if err != nil || string(body) != "mine" {
		t.Errorf("the existing file was touched: %q, %v", body, err)
	}
}

func TestGoldenExport(t *testing.T) {
	// Every format, every file: the goldens are the contract, so the whole tree
	// is compared, not only library.json.
	dir := filepath.Join(t.TempDir(), "export")
	opts := options(dir)
	if _, err := Write(context.Background(), fakeReader{library: fixtureLibrary()}, opts); err != nil {
		t.Fatalf("Write: %v", err)
	}
	golden := filepath.Join("testdata", "golden")

	compare := func(path, rel string) error {
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		wantPath := filepath.Join(golden, rel)
		if *update {
			if err := os.MkdirAll(filepath.Dir(wantPath), 0o755); err != nil {
				return err
			}
			return os.WriteFile(wantPath, got, 0o644)
		}
		want, err := os.ReadFile(wantPath)
		if err != nil {
			return fmt.Errorf("%s: %v (run `go test ./server/internal/export -update` and review the diff)", rel, err)
		}
		if string(got) != string(want) {
			return fmt.Errorf("%s does not match the golden export; review it and rerun with -update", rel)
		}
		return nil
	}

	seen := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		seen[rel] = true
		return compare(path, rel)
	})
	if err != nil {
		t.Error(err)
	}

	if !*update {
		// A golden with no file behind it is a stale contract.
		_ = filepath.WalkDir(golden, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(golden, path)
			if !seen[rel] {
				t.Errorf("golden %s is stale: the export no longer writes it", rel)
			}
			return nil
		})
	}
}

func TestMarkdownWritesOneFilePerItem(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "export")
	if _, err := Write(context.Background(), fakeReader{library: fixtureLibrary()}, options(dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "markdown", "0001-sourdough-pancakes.md"))
	if err != nil {
		t.Fatalf("the first item's markdown: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		`title: "Sourdough pancakes"`,
		`kind: "recipe"`,
		`tags: ["baking","breakfast"]`,
		`"id":"0190-space"`,
		"# Sourdough pancakes",
		"> Thin pancakes from starter discard. Good for a slow morning.",
		"## Note\n\nTry with the rye starter.",
		"## Content",
		"## Highlights\n\n> Rest, then cook.",
		"Fifteen minutes, not five.",
		"![pancakes.jpg](../media/",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("markdown lacks %q:\n%s", want, text)
		}
	}
}

func TestSlug(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Sourdough pancakes", "sourdough-pancakes"},
		{"  Spaced   out  ", "spaced-out"},
		{"C++ & Go: a comparison", "c-go-a-comparison"},
		{"", "untitled"},
		{"...", "untitled"},
		{"日本語", "untitled"},
		{strings.Repeat("long ", 40), "long-long-long-long-long-long-long-long-long-long-long-long"},
	} {
		if got := slug(tc.in); got != tc.want {
			t.Errorf("slug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNetscapeWritesFoldersDatesAndEscapes(t *testing.T) {
	library := fixtureLibrary()
	library.Items[1].Title = `Fish & "chips" <night>`
	library.Items[1].URL = stringPtr("https://kitchen.example/fish?a=1&b=2")
	dir := filepath.Join(t.TempDir(), "export")
	if _, err := Write(context.Background(), fakeReader{library: library}, options(dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "bookmarks.html"))
	if err != nil {
		t.Fatalf("bookmarks.html: %v", err)
	}
	text := string(body)

	if !strings.Contains(text, "<H3>Read later</H3>") {
		t.Errorf("the manual Space is not a folder:\n%s", text)
	}
	if !strings.Contains(text, `HREF="https://kitchen.example/fish?a=1&amp;b=2"`) {
		t.Errorf("the URL is not escaped:\n%s", text)
	}
	if !strings.Contains(text, `TAGS="baking,breakfast"`) {
		t.Errorf("tags are not carried:\n%s", text)
	}
	if !strings.Contains(text, "Fish &amp; &#34;chips&#34;") && !strings.Contains(text, "Fish &amp; &quot;chips&quot;") {
		t.Errorf("the title is not escaped:\n%s", text)
	}
	// ADD_DATE is unix seconds: the three clocks are what a browser sorts on.
	if want := fmt.Sprintf(`ADD_DATE="%d"`, fixedNow.Add(-72*time.Hour).Unix()); !strings.Contains(text, want) {
		t.Errorf("ADD_DATE is not the saved_at stamp (%s):\n%s", want, text)
	}
}

func TestNetscapeSkipsNotesAndDeletedItems(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "export")
	if _, err := Write(context.Background(), fakeReader{library: fixtureLibrary()}, options(dir)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "bookmarks.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "A thought") {
		t.Error("a note has no bookmark and must not be in bookmarks.html")
	}
	if strings.Contains(text, "Deleted but still mine") {
		t.Error("a soft-deleted item must not be handed to a browser")
	}
	if strings.Count(text, "<DT><A HREF") != 1 {
		t.Errorf("want exactly the one live URL item:\n%s", text)
	}
}

func stringPtr(s string) *string { return &s }
