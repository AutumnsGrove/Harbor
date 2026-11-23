package importer

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// The driver is plans/05 tests 1, 2, 6, 9 and 10: import twice and get one
// library, merge like a re-save, imported tags go through the shared write path,
// and imported items queue behind anything interactive.
//
// These run against a real SQLite store in a temp dir — the merge rules, the tag
// resolver and the FTS bookkeeping are the thing under test, so faking them
// would test the fake.

type env struct {
	store *store.Store
	ctx   context.Context
}

// testLogger is what the helpers need from a test: *testing.T and *rapid.T both
// satisfy it, so the property can use the same helpers as the examples.
type testLogger interface {
	Helper()
	Fatalf(format string, args ...any)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &env{store: s, ctx: context.Background()}
}

// runCSV imports a CSV body and returns the report.
func (e *env) runCSV(t testLogger, body string, opts Options) Report {
	t.Helper()
	src, err := NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatalf("NewCSVSource: %v", err)
	}
	report, err := Run(e.ctx, e.store, src, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

// newRapidEnv is newEnv for a property: rapid draws its own test object, so the
// temp dir and the cleanup are spelled out.
func newRapidEnv(t *rapid.T) (*env, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "harbor-import-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	s, err := store.Open(dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("store.Open: %v", err)
	}
	return &env{store: s, ctx: context.Background()}, func() {
		_ = s.Close()
		_ = os.RemoveAll(dir)
	}
}

func (e *env) library(t testLogger) model.Library {
	t.Helper()
	library, err := e.store.ExportLibrary(e.ctx)
	if err != nil {
		t.Fatalf("ExportLibrary: %v", err)
	}
	return library
}

func (e *env) item(t testLogger, url string) model.Item {
	t.Helper()
	item, err := e.store.GetItemByURL(e.ctx, url)
	if err != nil {
		t.Fatalf("GetItemByURL(%q): %v", url, err)
	}
	return item
}

func (e *env) spaceNames(t testLogger, itemID string) map[string]bool {
	t.Helper()
	library := e.library(t)
	byID := map[string]string{}
	for _, space := range library.Spaces {
		byID[space.ID] = space.Name
	}
	names := map[string]bool{}
	for spaceID, items := range library.SpaceItems {
		for _, id := range items {
			if id == itemID {
				names[byID[spaceID]] = true
			}
		}
	}
	return names
}

// A row becomes an item with everything the export carried: the old date, the
// tags, the folder as a Space, the highlight rows, and the priority that keeps
// it behind interactive saves (plans/05 tests 5 and 10).
func TestRunImportsARow(t *testing.T) {
	e := newEnv(t)
	report := e.runCSV(t, `1,Sourdough,feed it daily,,https://example.com/sourdough,Recipes,"baking, bread",2020-03-04T05:06:07.890Z,https://img.example.com/1.jpg,Highlight:first passage,false
`, Options{})

	if report.Imported != 1 || report.Merged != 0 || report.Failed != 0 {
		t.Fatalf("report = %+v, want one import and nothing else", report)
	}
	if report.Highlights != 1 {
		t.Errorf("report highlights = %d, want 1: the count the UI shows after a real run", report.Highlights)
	}
	if report.SpacesCreated != 1 {
		t.Errorf("spaces created = %d, want 1", report.SpacesCreated)
	}

	item := e.item(t, "https://example.com/sourdough")
	if item.Title != "Sourdough" || item.Note != "feed it daily" {
		t.Errorf("item = %q / %q", item.Title, item.Note)
	}
	if item.Source != model.SourceImport {
		t.Errorf("source = %q, want import", item.Source)
	}
	if item.ImportRef != "1" {
		t.Errorf("import_ref = %q, want the Raindrop id", item.ImportRef)
	}
	if item.Priority != -1 {
		t.Errorf("priority = %d, want -1", item.Priority)
	}
	wantDate := time.Date(2020, 3, 4, 5, 6, 7, 890_000_000, time.UTC)
	if !item.CreatedAt.Equal(wantDate) || !item.SavedAt.Equal(wantDate) {
		t.Errorf("created/saved = %v / %v, want the Raindrop date (plans/05 risk: backdated dates)",
			item.CreatedAt, item.SavedAt)
	}
	if item.State != model.StateQueued {
		t.Errorf("state = %q, want queued for enrichment", item.State)
	}
	if !strings.Contains(item.OGJSON, "img.example.com/1.jpg") {
		t.Errorf("og_json = %q, want the cover", item.OGJSON)
	}

	tags, err := e.store.TagsForItem(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tags, ","); got != "baking,bread" {
		t.Errorf("tags = %q, want baking,bread", got)
	}

	if names := e.spaceNames(t, item.ID); !names["Recipes"] {
		t.Errorf("spaces = %v, want Recipes", names)
	}

	highlights, err := e.store.HighlightsForItem(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(highlights) != 1 || highlights[0].Quote != "first passage" {
		t.Errorf("highlights = %+v, want the imported passage", highlights)
	}
}

// S9: favourites are a real thing, so a favourite row joins the seeded Space.
func TestRunFilesFavourites(t *testing.T) {
	e := newEnv(t)
	e.runCSV(t, `1,Favourite,,,https://example.com/fav,Articles,,2021-01-01T00:00:00Z,,,true
2,Ordinary,,,https://example.com/plain,Articles,,2021-01-01T00:00:00Z,,,false
`, Options{})

	fav := e.item(t, "https://example.com/fav")
	if names := e.spaceNames(t, fav.ID); !names["Favourites"] {
		t.Errorf("spaces = %v, want Favourites", names)
	}
	plain := e.item(t, "https://example.com/plain")
	if names := e.spaceNames(t, plain.ID); names["Favourites"] {
		t.Errorf("a non-favourite joined Favourites: %v", names)
	}
}

// plans/05 test 2: a URL already in the library gains the imported note and
// tags, keeps its own date, its saved_at, its AI tags and Read later.
func TestRunMergesIntoAnExistingItem(t *testing.T) {
	e := newEnv(t)
	savedAt := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	existing, _, err := e.store.MergeItem(e.ctx, store.ItemInput{
		ID:        "existing",
		URL:       ptr("https://example.com/a"),
		Title:     "My title",
		Note:      "my own note",
		Tags:      []string{"mine"},
		ReadLater: true,
		CreatedAt: time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC),
		SavedAt:   savedAt,
	})
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	// An AI tag, which an import must not lose.
	if _, err := e.store.SetItemTags(e.ctx, existing.ID, []string{"ai-tag"}, model.TagSourceAI); err != nil {
		t.Fatalf("SetItemTags: %v", err)
	}

	report := e.runCSV(t, `9,Imported,imported note,,https://example.com/a,Reference,"imported, mine",2019-05-05T05:05:05Z,,,false
`, Options{})
	if report.Merged != 1 || report.Imported != 0 {
		t.Fatalf("report = %+v, want one merge", report)
	}

	merged := e.item(t, "https://example.com/a")
	if merged.ID != existing.ID {
		t.Fatalf("merge created a second item: %q vs %q", merged.ID, existing.ID)
	}
	if !strings.Contains(merged.Note, "my own note") || !strings.Contains(merged.Note, "imported note") {
		t.Errorf("note = %q, want both notes", merged.Note)
	}
	if !merged.CreatedAt.Equal(time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("created_at = %v, want the earlier date kept", merged.CreatedAt)
	}
	if !merged.SavedAt.Equal(savedAt) {
		t.Errorf("saved_at = %v, want %v: an import must not move an item back to 2019", merged.SavedAt, savedAt)
	}
	if in := e.spaceNames(t, merged.ID); !in["Read later"] {
		t.Errorf("spaces = %v, want Read later kept", in)
	}
	tags, err := e.store.TagsForItem(e.ctx, merged.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tags, ","); got != "ai-tag,imported,mine" {
		t.Errorf("tags = %q, want the union with the AI tag kept", got)
	}
}

// The headline property (plans/05 test 1): one pass and two passes produce the
// same library. rapid generates the file; the export is the comparison.
func TestPropertyImportTwiceIsOneLibrary(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		e, cleanup := newRapidEnv(t)
		defer cleanup()
		body := genCSV(t)
		first := e.runCSV(t, body, Options{})
		before := e.library(t)

		second := e.runCSV(t, body, Options{})
		after := e.library(t)

		if !reflect.DeepEqual(before, after) {
			t.Fatalf("a second import changed the library\nfile:\n%s\nfirst report: %+v\nsecond report: %+v\n%s",
				body, first, second, diffLibraries(before, after))
		}
		if second.Imported != 0 && first.Imported != 0 {
			// Every row of the second pass must merge (or skip), never import.
			if second.Imported != 0 {
				t.Fatalf("second pass imported %d new items: %+v", second.Imported, second)
			}
		}
	})
}

// genCSV draws a small library-shaped file: folders, tags, notes, highlights,
// favourites, a duplicate URL, and URLs that differ only by spelling.
func genCSV(t *rapid.T) string {
	folders := []string{"", "Articles", "Read Later", "Recipes", "recipes"}
	tags := []string{"", "baking", "Baking, bread", "a, b, c", "3D, 3d", "hidden gems"}
	rows := rapid.IntRange(1, 8).Draw(t, "rows")

	var b strings.Builder
	for i := 0; i < rows; i++ {
		// A small URL space on purpose: duplicates and near-duplicates exercise
		// the merge path instead of only the insert path.
		url := fmt.Sprintf("https://example.com/page/%d", rapid.IntRange(1, 4).Draw(t, "page"))
		if rapid.Bool().Draw(t, "utm") {
			url += "?utm_source=" + rapid.SampledFrom([]string{"x", "feed"}).Draw(t, "utmval")
		}
		if rapid.Bool().Draw(t, "fragment") {
			url += "#section"
		}
		note := rapid.SampledFrom([]string{"", "a note", "two\nlines", "with, a comma"}).Draw(t, "note")
		highlights := rapid.SampledFrom([]string{"", "Highlight:one", "Highlight:one\n\nHighlight:two words"}).Draw(t, "highlights")

		rec := []string{
			fmt.Sprintf("id%d", i),
			rapid.StringMatching(`[A-Za-z ]{0,12}`).Draw(t, "title"),
			note,
			"",
			url,
			rapid.SampledFrom(folders).Draw(t, "folder"),
			rapid.SampledFrom(tags).Draw(t, "tags"),
			rapid.SampledFrom([]string{"2019-01-02T03:04:05Z", "2022-06-07T08:09:10.011Z", "2024-12-31T23:59:59Z"}).Draw(t, "created"),
			"",
			highlights,
			rapid.SampledFrom([]string{"true", "false"}).Draw(t, "favorite"),
		}
		var line strings.Builder
		w := csv.NewWriter(&line)
		if err := w.Write(rec); err != nil {
			t.Fatalf("writing the fixture row: %v", err)
		}
		w.Flush()
		b.WriteString(line.String())
	}
	return b.String()
}

// A dry run reports what would happen and writes nothing — it is what gets run
// against the real export before anything is committed (plans/05 tasks).
func TestRunDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	body := `1,New one,,,https://example.com/new,Recipes,,2020-01-01T00:00:00Z,,,false
2,New favourite,,,https://example.com/fav,Articles,,2020-01-01T00:00:00Z,,,true
3,Merge me,,,https://example.com/existing,Recipes,,2019-01-01T00:00:00Z,,,false
`
	if _, err := e.store.CreateItem(e.ctx, store.ItemInput{
		ID: "existing", URL: ptr("https://example.com/existing"), Title: "Existing",
	}); err != nil {
		t.Fatal(err)
	}
	before := e.library(t)

	report := e.runCSV(t, body, Options{DryRun: true})
	if report.Imported != 2 || report.Merged != 1 || report.Total != 3 {
		t.Errorf("report = %+v, want 2 new, 1 merge, 3 rows", report)
	}
	if report.SpacesToCreate != 2 {
		t.Errorf("spaces to create = %d, want 2 (Articles, Recipes)", report.SpacesToCreate)
	}
	if report.Favourites != 1 {
		t.Errorf("favourites = %d, want 1", report.Favourites)
	}
	if after := e.library(t); !reflect.DeepEqual(before, after) {
		t.Error("a dry run changed the library")
	}
}

// A row that cannot become an item is counted and reported, and the rows around
// it are still imported (plans/05 test 3's property, at the driver level).
func TestRunRowFailuresDoNotStopTheRun(t *testing.T) {
	e := newEnv(t)
	report := e.runCSV(t, `1,Good,,,https://example.com/good,Articles,,2020-01-01T00:00:00Z,,,false
2,Not a URL,,,javascript:alert(1),Articles,,2020-01-01T00:00:00Z,,,false
3,No url at all,,,,Articles,,2020-01-01T00:00:00Z,,,false
4,Also good,,,https://example.com/also,Articles,,2020-01-01T00:00:00Z,,,false
`, Options{})

	if report.Imported != 2 {
		t.Errorf("imported = %d, want 2", report.Imported)
	}
	if report.Failed != 1 {
		t.Errorf("failed = %d, want 1 (the javascript: URL)", report.Failed)
	}
	if report.Skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the row with no URL)", report.Skipped)
	}
	if len(report.Errors) != 1 || report.Errors[0].Record != 2 {
		t.Errorf("errors = %+v, want the row 2 failure", report.Errors)
	}
}

// --no-enrich writes items that are usable immediately rather than queued
// (plans/05 tasks).
func TestRunNoEnrichMarksItemsReady(t *testing.T) {
	e := newEnv(t)
	e.runCSV(t, `1,Ready now,,,https://example.com/ready,Articles,,2020-01-01T00:00:00Z,,,false
`, Options{NoEnrich: true})

	if item := e.item(t, "https://example.com/ready"); item.State != model.StateReady {
		t.Errorf("state = %q, want ready", item.State)
	}
}

func TestRunLimit(t *testing.T) {
	e := newEnv(t)
	report := e.runCSV(t, `1,One,,,https://example.com/1,A,,2020-01-01T00:00:00Z,,,false
2,Two,,,https://example.com/2,A,,2020-01-01T00:00:00Z,,,false
3,Three,,,https://example.com/3,A,,2020-01-01T00:00:00Z,,,false
`, Options{Limit: 2})

	if report.Imported != 2 {
		t.Errorf("imported = %d, want 2", report.Imported)
	}
	if n := len(e.library(t).Items); n != 2 {
		t.Errorf("%d items in the library, want 2", n)
	}
}

// Imported tags are the owner's, not the model's, and they land as manual tags
// so a later enrichment cannot erase them (plans/05 test 9's other half).
func TestRunTagsAreManual(t *testing.T) {
	e := newEnv(t)
	e.runCSV(t, `1,A,,,https://example.com/tags,,"baking, bread",2020-01-01T00:00:00Z,,,false
`, Options{})

	item := e.item(t, "https://example.com/tags")
	tags, err := e.store.TagsForItemWithSource(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range tags {
		if source != model.TagSourceManual {
			t.Errorf("tag %q has source %q, want manual", name, source)
		}
	}
	if len(tags) != 2 {
		t.Errorf("tags = %v, want two", tags)
	}
}

// Imported items are a backlog: a link saved interactively afterwards is
// enriched first (plans/05 test 10).
func TestImportedItemsQueueBehindInteractiveSaves(t *testing.T) {
	e := newEnv(t)
	e.runCSV(t, `1,Imported,,,https://example.com/imported,A,,2020-01-01T00:00:00Z,,,false
`, Options{})
	if _, err := e.store.CreateItem(e.ctx, store.ItemInput{
		ID: "interactive", URL: ptr("https://example.com/interactive"), Title: "Interactive",
	}); err != nil {
		t.Fatal(err)
	}

	queued, err := e.store.QueuedItems(e.ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 {
		t.Fatalf("queued = %d, want 2", len(queued))
	}
	if queued[0].ID != "interactive" {
		t.Errorf("first in the queue = %q, want the interactive save", queued[0].ID)
	}
}

func TestRunRejectsAnEmptySource(t *testing.T) {
	e := newEnv(t)
	_, err := NewCSVSource(strings.NewReader(""))
	if !errors.Is(err, ErrNotRaindropCSV) {
		t.Errorf("err = %v, want ErrNotRaindropCSV", err)
	}
	// A source that ends immediately is an empty import, not an error.
	src, err := NewCSVSource(strings.NewReader(header))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(e.ctx, e.store, src, Options{})
	if err != nil || report.Total != 0 {
		t.Errorf("report = %+v, err = %v; want an empty successful run", report, err)
	}
}

func ptr(s string) *string { return &s }

// diffLibraries says what a second pass changed, so a property failure is
// readable without a debugger.
func diffLibraries(before, after model.Library) string {
	var out strings.Builder
	for i := range before.Items {
		if i >= len(after.Items) {
			out.WriteString("an item disappeared\n")
			break
		}
		if !reflect.DeepEqual(before.Items[i], after.Items[i]) {
			fmt.Fprintf(&out, "item %s changed:\n before %+v\n after  %+v\n", before.Items[i].ID, before.Items[i], after.Items[i])
		}
	}
	if !reflect.DeepEqual(before.Highlights, after.Highlights) {
		fmt.Fprintf(&out, "highlights:\n before %+v\n after  %+v\n", before.Highlights, after.Highlights)
	}
	if !reflect.DeepEqual(before.Spaces, after.Spaces) {
		fmt.Fprintf(&out, "spaces:\n before %+v\n after  %+v\n", before.Spaces, after.Spaces)
	}
	if !reflect.DeepEqual(before.SpaceItems, after.SpaceItems) {
		fmt.Fprintf(&out, "space items:\n before %+v\n after  %+v\n", before.SpaceItems, after.SpaceItems)
	}
	if !reflect.DeepEqual(before.Tags, after.Tags) {
		fmt.Fprintf(&out, "tags:\n before %+v\n after  %+v\n", before.Tags, after.Tags)
	}
	return out.String()
}

// A source that cannot express highlights must not clear the ones an earlier
// import wrote: plans/05 imports the Netscape HTML twin of the same library over
// the CSV's result, and design calls highlights the part she cannot recreate.
func TestRunWithoutHighlightsDoesNotClearThem(t *testing.T) {
	e := newEnv(t)
	e.runCSV(t, `1,Cut ,Highlight:one,,"https://example.com/a",,,"2020-01-01T00:00:00Z",,"Highlight:one",false
`, Options{})
	item := e.item(t, "https://example.com/a")
	before, err := e.store.HighlightsForItem(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("highlights = %d, want 1", len(before))
	}

	// A CSV without a highlights column at all is the shape of a highlight-free
	// source; the HTML reader answers the same way.
	src, err := NewCSVSource(strings.NewReader("id,title,url\n2,Same page,https://example.com/a\n"))
	if err != nil {
		t.Fatalf("NewCSVSource: %v", err)
	}
	if src.CarriesHighlights() {
		t.Fatal("a file with no highlights column claims to carry highlights")
	}
	if _, err := Run(e.ctx, e.store, src, Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	after, err := e.store.HighlightsForItem(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Quote != "one" {
		t.Errorf("highlights = %+v, want the imported one kept", after)
	}
}

// The same file with an empty cell on a highlights-bearing source does clear
// them: there the file is the truth.
func TestRunClearsHighlightsTheFileDropped(t *testing.T) {
	e := newEnv(t)
	e.runCSV(t, `1,A,,,"https://example.com/a",,,"2020-01-01T00:00:00Z",,"Highlight:one",false
`, Options{})
	e.runCSV(t, `1,A,,,"https://example.com/a",,,"2020-01-01T00:00:00Z",,,false
`, Options{})

	item := e.item(t, "https://example.com/a")
	got, err := e.store.HighlightsForItem(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("highlights = %+v, want none: the file no longer carries one", got)
	}
}

// The dry run's new-vs-merge headline has to match the real run, including when
// two rows canonicalise to the same URL.
func TestRunDryRunCountsDuplicateURLsAsMerges(t *testing.T) {
	e := newEnv(t)
	body := `1,One,,,https://example.com/a?utm_source=x,A,,2020-01-01T00:00:00Z,,,false
2,Two,,,https://example.com/a#frag,A,,2020-01-01T00:00:00Z,,,false
`
	dry := e.runCSV(t, body, Options{DryRun: true})
	if dry.Imported != 1 || dry.Merged != 1 {
		t.Errorf("dry run = %d new / %d merged, want 1 / 1", dry.Imported, dry.Merged)
	}
	real := e.runCSV(t, body, Options{})
	if real.Imported != dry.Imported || real.Merged != dry.Merged {
		t.Errorf("real run = %d new / %d merged, dry run said %d / %d",
			real.Imported, real.Merged, dry.Imported, dry.Merged)
	}
}

// The counters describe the file, so they add up to the number of rows read.
func TestRunCountersAddUp(t *testing.T) {
	e := newEnv(t)
	report := e.runCSV(t, `1,Good,,,https://example.com/good,A,,2020-01-01T00:00:00Z,,,false
2,Nope,,,javascript:alert(1),A,,2020-01-01T00:00:00Z,,,false
3,No url,,,,A,,2020-01-01T00:00:00Z,,,false
4,Bad date,,,https://example.com/date,A,,not-a-date,,,false
`, Options{})
	if got := report.Imported + report.Merged + report.Skipped + report.Failed; got != report.Total {
		t.Errorf("imported+merged+skipped+failed = %d, total = %d (report %+v)", got, report.Total, report)
	}
	// A row whose date will not parse is imported, but the report says so.
	if !hasFileWarning(report.FileWarnings, "record 4", "created") {
		t.Errorf("warnings = %v, want the bad date surfaced", report.FileWarnings)
	}
}

// SpacesCreated is what the UI shows; a folder reused by the second pass is not
// a Space created by it.
func TestRunCountsSpacesCreated(t *testing.T) {
	e := newEnv(t)
	body := `1,A,,,https://example.com/1,Recipes,,2020-01-01T00:00:00Z,,,false
2,B,,,https://example.com/2,Recipes,,2020-01-01T00:00:00Z,,,false
`
	first := e.runCSV(t, body, Options{})
	if first.SpacesCreated != 1 {
		t.Errorf("spaces created = %d, want 1 for the one new folder", first.SpacesCreated)
	}
	second := e.runCSV(t, body, Options{})
	if second.SpacesCreated != 0 {
		t.Errorf("spaces created = %d on a re-run, want 0", second.SpacesCreated)
	}
}

func hasFileWarning(warnings []string, parts ...string) bool {
	for _, warning := range warnings {
		matched := true
		for _, part := range parts {
			if !strings.Contains(warning, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// The HTML twin of the same library imports into the same items — merging, not
// duplicating — and leaves the highlights the CSV import wrote (plans/05 exit
// criterion 5). This is the end-to-end shape of the round trip.
func TestRunImportsTheHTMLTwinOverTheCSV(t *testing.T) {
	e := newEnv(t)
	e.runCSV(t, `1,Sourdough,my note,,https://example.com/sourdough,Recipes,"baking, bread",2020-03-04T05:06:07Z,,Highlight:one,true
`, Options{})
	csvItem := e.item(t, "https://example.com/sourdough")
	before := e.library(t)

	html := `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<DL><p>
    <DT><A HREF="https://example.com/sourdough" ADD_DATE="1583298367" TAGS="baking,bread">Sourdough</A>
</DL><p>
`
	src, err := NewHTMLSource(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	if src.CarriesHighlights() {
		t.Fatal("the HTML source claims to carry highlights")
	}
	report, err := Run(e.ctx, e.store, src, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Merged != 1 || report.Imported != 0 {
		t.Errorf("report = %+v, want the one bookmark merged into the CSV's item", report)
	}

	after := e.library(t)
	if len(after.Items) != len(before.Items) {
		t.Fatalf("the HTML import made %d items, want %d", len(after.Items), len(before.Items))
	}
	item := e.item(t, "https://example.com/sourdough")
	if item.ID != csvItem.ID {
		t.Errorf("the HTML import created a second item: %q vs %q", item.ID, csvItem.ID)
	}
	if !strings.Contains(item.Note, "my note") {
		t.Errorf("note = %q, want the CSV note kept", item.Note)
	}
	highlights, err := e.store.HighlightsForItem(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(highlights) != 1 {
		t.Errorf("highlights = %+v, want the imported one kept across the HTML pass", highlights)
	}
	if names := e.spaceNames(t, item.ID); !names["Recipes"] || !names["Favourites"] {
		t.Errorf("spaces = %v, want Recipes and Favourites kept", names)
	}
}

// plans/05 test 6: a cancelled run resumes from its checkpoint rather than
// starting over, and the rows it skips were already applied.
func TestRunResumesFromTheCheckpoint(t *testing.T) {
	e := newEnv(t)
	body := `1,One,H1,,"https://example.com/1",A,,"2020-01-01T00:00:00Z",,Highlight:one,false
2,Two,H2,,"https://example.com/2",A,,"2020-01-01T00:00:00Z",,Highlight:two,false
3,Three,H3,,"https://example.com/3",A,,"2020-01-01T00:00:00Z",,Highlight:three,false
`
	// The first run got through two rows and was cancelled at its checkpoint.
	src, err := NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatal(err)
	}
	var checkpoints []int
	first, err := Run(e.ctx, e.store, src, Options{
		Limit:           2,
		CheckpointEvery: 1,
		OnCheckpoint: func(records int, _ Report) error {
			checkpoints = append(checkpoints, records)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Imported != 2 || len(checkpoints) != 2 || checkpoints[1] != 2 {
		t.Fatalf("first run = %+v, checkpoints = %v", first, checkpoints)
	}
	// The checkpoint flush wrote the highlights for the rows it had read.
	if got, err := e.store.HighlightsForItem(e.ctx, e.item(t, "https://example.com/1").ID); err != nil || len(got) != 1 {
		t.Fatalf("row 1 highlights = %+v, %v; the checkpoint got ahead of them", got, err)
	}

	// The resume skips those two rows by checkpoint and imports the third.
	src, err = NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(e.ctx, e.store, src, Options{SkipRows: 2})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if second.Total != 1 || second.Imported != 1 {
		t.Errorf("resume = %+v, want the one remaining row", second)
	}
	if n := len(e.library(t).Items); n != 3 {
		t.Errorf("%d items after the resume, want 3", n)
	}
}

// A cancelled context stops the run where it is, without losing what it wrote
// and without claiming the row it was writing failed.
func TestRunStopsOnCancellation(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(e.ctx)
	body := `1,One,,,"https://example.com/1",A,,"2020-01-01T00:00:00Z",,,false
2,Two,,,"https://example.com/2",A,,"2020-01-01T00:00:00Z",,,false
`
	src, err := NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(ctx, e.store, src, Options{
		CheckpointEvery: 1,
		OnCheckpoint: func(records int, _ Report) error {
			if records == 1 {
				cancel()
			}
			return nil
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if report.Imported != 1 {
		t.Errorf("report = %+v, want the one row that was written", report)
	}
	// The interrupted row is the run stopping, not a failed row: counting it as
	// failed would leave it behind the checkpoint, and the resume would skip it.
	if report.Failed != 0 {
		t.Errorf("report = %+v, want no failed rows for a cancellation", report)
	}
	if n := len(e.library(t).Items); n != 1 {
		t.Errorf("%d items after the cancel, want 1", n)
	}
}

// A URL that appears on both sides of a checkpoint keeps every quote it has: a
// resumed run must not flush a partial set over what the first pass wrote.
func TestRunResumeKeepsHighlightsFromBothSidesOfTheCheckpoint(t *testing.T) {
	e := newEnv(t)
	body := `1,First,H1,,"https://example.com/same",A,,"2020-01-01T00:00:00Z",,Highlight:one,false
2,Other,H2,,"https://example.com/other",A,,"2020-01-01T00:00:00Z",,Highlight:other,false
3,Third,H3,,"https://example.com/same",A,,"2020-01-01T00:00:00Z",,Highlight:three,false
`
	// A single pass keeps both quotes.
	src, err := NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(e.ctx, e.store, src, Options{}); err != nil {
		t.Fatalf("single pass: %v", err)
	}
	single, err := e.store.HighlightsForItem(e.ctx, e.item(t, "https://example.com/same").ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != 2 {
		t.Fatalf("single pass kept %d quotes, want 2", len(single))
	}

	// Now the same file through a checkpoint at row 2 and a resume: the same two
	// quotes, in the same order.
	e2 := newEnv(t)
	src, err = NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(e2.ctx, e2.store, src, Options{Limit: 2}); err != nil {
		t.Fatalf("first half: %v", err)
	}
	src, err = NewCSVSource(strings.NewReader(header + body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(e2.ctx, e2.store, src, Options{SkipRows: 2}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	resumed, err := e2.store.HighlightsForItem(e2.ctx, e2.item(t, "https://example.com/same").ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 2 {
		t.Fatalf("the resume left %d quotes, want 2 (the first pass's must survive)", len(resumed))
	}
	for i := range single {
		if resumed[i].Quote != single[i].Quote {
			t.Errorf("quote %d = %q, want %q", i, resumed[i].Quote, single[i].Quote)
		}
	}
}

// A dry run writes nothing, highlights included — not even the fallback that a
// resumed run uses to fill in an item it never wrote.
func TestRunDryRunDoesNotTouchHighlights(t *testing.T) {
	e := newEnv(t)
	e.runCSV(t, `1,Seed,,,"https://example.com/a",,,"2020-01-01T00:00:00Z",,Highlight:old,false
`, Options{})
	item := e.item(t, "https://example.com/a")

	// The file now says something different; the preview must not apply it.
	dry := e.runCSV(t, `2,Seed,,,"https://example.com/a",,,"2020-01-01T00:00:00Z",,Highlight:new,false
`, Options{DryRun: true})
	if dry.Imported != 0 || dry.Merged != 1 {
		t.Fatalf("dry run = %+v, want one merge", dry)
	}

	got, err := e.store.HighlightsForItem(e.ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Quote != "old" {
		t.Errorf("highlights = %+v, want the stored one untouched by the dry run", got)
	}
}
