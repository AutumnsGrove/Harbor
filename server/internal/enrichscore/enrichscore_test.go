package enrichscore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// plans/03, "Scored fixture set": ~40 real saves with a human kind (and, where
// the owner wrote them, human tags). The score is the phase's real result, so
// the arithmetic, the gate and the fixture format are all pinned here.

func approx(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func labelled(id string, kind model.Kind, tags ...string) Case {
	return Case{ID: id, URL: "https://example.test/" + id, Text: "text", Kind: kind, Tags: tags}
}

func answered(id string, kind model.Kind, tags ...string) Answer {
	return Answer{ID: id, Kind: kind, Tags: tags}
}

func TestScoreCountsKindAccuracy(t *testing.T) {
	cases := []Case{
		labelled("a", model.KindArticle),
		labelled("b", model.KindRecipe),
		labelled("c", model.KindVideo),
		labelled("d", model.KindProduct),
	}
	report := Score(cases, []Answer{
		answered("a", model.KindArticle),
		answered("b", model.KindRecipe),
		answered("c", model.KindArticle),
		answered("d", model.KindBook),
	})
	if report.Labelled != 4 || report.Correct != 2 {
		t.Fatalf("labelled/correct = %d/%d, want 4/2", report.Labelled, report.Correct)
	}
	approx(t, report.Accuracy, 0.5)
}

func TestScoreKeepsPerKindConfusion(t *testing.T) {
	cases := []Case{
		labelled("a", model.KindArticle),
		labelled("b", model.KindArticle),
		labelled("c", model.KindRecipe),
	}
	report := Score(cases, []Answer{
		answered("a", model.KindArticle),
		answered("b", model.KindVideo),
		answered("c", model.KindRecipe),
	})
	article := kindRow(t, report, model.KindArticle)
	if article.Labelled != 2 || article.Correct != 1 {
		t.Errorf("article = %d labelled, %d correct, want 2/1", article.Labelled, article.Correct)
	}
	if article.Confused[model.KindVideo] != 1 {
		t.Errorf("article confusion = %v, want one video", article.Confused)
	}
	if _, ok := report.Kinds[model.KindProduct]; ok {
		t.Error("a kind with no labelled cases should not appear")
	}
}

func TestScoreCountsAMissingAnswerAsWrong(t *testing.T) {
	cases := []Case{labelled("a", model.KindArticle), labelled("b", model.KindRecipe)}
	report := Score(cases, []Answer{answered("a", model.KindArticle)})

	if report.Correct != 1 || report.Labelled != 2 {
		t.Fatalf("labelled/correct = %d/%d, want 2/1", report.Labelled, report.Correct)
	}
	if len(report.Errors) != 1 {
		t.Fatalf("errors = %v, want one for the unanswered case", report.Errors)
	}
	if report.Passes() {
		t.Error("a run with a missing answer must not pass: in production that is a failed save")
	}
}

func TestScoreReportsAFailureAsWrongAndNamed(t *testing.T) {
	report := Score([]Case{labelled("a", model.KindArticle)}, []Answer{
		{ID: "a", Err: errors.New("nova classify: 429")},
	})
	if report.Correct != 0 {
		t.Errorf("correct = %d, want 0", report.Correct)
	}
	if len(report.Errors) != 1 || !strings.Contains(report.Errors[0], "429") {
		t.Errorf("errors = %v, want the call's cause", report.Errors)
	}
	if !strings.Contains(report.String(), "429") {
		t.Errorf("the report hides the failure:\n%s", report)
	}
}

func TestScoreIgnoresUnlabelledCases(t *testing.T) {
	cases := []Case{
		labelled("a", model.KindArticle),
		{ID: "b", URL: "https://example.test/b", Text: "text"},
	}
	report := Score(cases, []Answer{
		answered("a", model.KindArticle),
		answered("b", model.KindRecipe),
	})
	if report.Labelled != 1 || report.Unlabelled != 1 || report.Correct != 1 {
		t.Errorf("labelled/unlabelled/correct = %d/%d/%d, want 1/1/1",
			report.Labelled, report.Unlabelled, report.Correct)
	}
	approx(t, report.Accuracy, 1)
}

func TestScoreReportsTagRecallOverTaggedCases(t *testing.T) {
	cases := []Case{
		labelled("a", model.KindArticle, "climate", "policy", "europe"),
		labelled("b", model.KindRecipe), // kind only: contributes to accuracy, not tags
	}
	report := Score(cases, []Answer{
		answered("a", model.KindArticle, "Climate", "policy"),
		answered("b", model.KindRecipe, "baking"),
	})
	if report.Tagged != 1 {
		t.Fatalf("tagged = %d, want only the case with human tags", report.Tagged)
	}
	approx(t, report.TagRecall, 2.0/3.0)
	if got := report.String(); !strings.Contains(got, "tag recall") {
		t.Errorf("the report omits the tag line:\n%s", got)
	}
}

func TestPassesNeedsEightyPercentKindAccuracy(t *testing.T) {
	eight := make([]Case, 10)
	answers := make([]Answer, 10)
	for i := range eight {
		id := "c" + string(rune('a'+i))
		eight[i] = labelled(id, model.KindArticle)
		answers[i] = answered(id, model.KindArticle)
	}
	if !Score(eight, answers).Passes() {
		t.Fatal("10/10 must pass")
	}

	answers[3] = answered(answers[3].ID, model.KindVideo)
	answers[4] = answered(answers[4].ID, model.KindVideo)
	if report := Score(eight, answers); !report.Passes() {
		t.Errorf("8/10 is exactly the gate and must pass:\n%s", report)
	}

	answers[5] = answered(answers[5].ID, model.KindVideo)
	if report := Score(eight, answers); report.Passes() {
		t.Errorf("7/10 is under the gate and must fail:\n%s", report)
	}
}

func TestTheReportNamesTheGateAndTheConfusion(t *testing.T) {
	report := Score(
		[]Case{labelled("a", model.KindArticle), labelled("b", model.KindArticle)},
		[]Answer{answered("a", model.KindArticle), answered("b", model.KindVideo)},
	)
	got := report.String()
	for _, want := range []string{"80", "article", "video", "50.0%"} {
		if !strings.Contains(got, want) {
			t.Errorf("report lacks %q:\n%s", want, got)
		}
	}
}

func TestAnEmptyRunFails(t *testing.T) {
	if Score(nil, nil).Passes() {
		t.Error("nothing scored is not a pass")
	}
}

func kindRow(t *testing.T, report Report, kind model.Kind) KindRow {
	t.Helper()
	row, ok := report.Kinds[kind]
	if !ok {
		t.Fatalf("no row for %q in %v", kind, report.Kinds)
	}
	return row
}

// --- loading the committed set ---------------------------------------------

const caseJSON = `{
  "url": "https://kitchen.example/pancakes",
  "captured": "2026-11-22",
  "title": "Sourdough pancakes",
  "siteName": "Kitchen",
  "excerpt": "A use for the discard.",
  "text": "Whisk the starter, egg and milk."
}`

func writeSet(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	return dir
}

func TestLoadReadsCasesAndJoinsTheLabels(t *testing.T) {
	dir := writeSet(t, map[string]string{
		"pancakes.json": caseJSON,
		"labels.json":   `{"pancakes": {"kind": "recipe", "tags": ["Baking", "bread"]}}`,
	})
	cases, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("cases = %d, want 1", len(cases))
	}
	got := cases[0]
	if got.ID != "pancakes" || got.Kind != model.KindRecipe {
		t.Errorf("id/kind = %q/%q", got.ID, got.Kind)
	}
	if got.URL != "https://kitchen.example/pancakes" || !strings.Contains(got.Text, "Whisk") {
		t.Errorf("page not read: %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "baking" {
		t.Errorf("tags = %v, want them normalised", got.Tags)
	}
}

func TestLoadAcceptsAnUnlabelledCase(t *testing.T) {
	dir := writeSet(t, map[string]string{"pancakes.json": caseJSON})
	cases, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cases) != 1 || cases[0].Kind != "" {
		t.Fatalf("cases = %+v, want one unlabelled", cases)
	}
}

func TestLoadTreatsABlankKindAsUnlabelled(t *testing.T) {
	// -record writes every id into labels.json with a blank kind so the owner
	// has one file to fill in; a blank means "not labelled yet", not an error.
	dir := writeSet(t, map[string]string{
		"pancakes.json": caseJSON,
		"labels.json":   `{"pancakes": {"kind": "", "tags": []}}`,
	})
	cases, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cases) != 1 || cases[0].Kind != "" {
		t.Fatalf("cases = %+v, want one unlabelled", cases)
	}
}

func TestLoadRejectsAnUnknownKindInTheLabels(t *testing.T) {
	dir := writeSet(t, map[string]string{
		"pancakes.json": caseJSON,
		"labels.json":   `{"pancakes": {"kind": "food"}}`,
	})
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "food") {
		t.Fatalf("Load = %v, want a complaint naming the bad kind", err)
	}
}

func TestLoadRejectsALabelWithNoCase(t *testing.T) {
	dir := writeSet(t, map[string]string{
		"pancakes.json": caseJSON,
		"labels.json":   `{"waffles": {"kind": "recipe"}}`,
	})
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "waffles") {
		t.Fatalf("Load = %v, want a complaint naming the orphan label", err)
	}
}

func TestLoadRejectsACaseWithNoText(t *testing.T) {
	dir := writeSet(t, map[string]string{
		"empty.json": `{"url": "https://example.test/empty", "text": "  "}`,
	})
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("Load = %v, want a complaint about the empty capture", err)
	}
}

func TestLoadRejectsASetWithNoCases(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load of an empty directory should fail")
	}
}

// TestLoadOfTheCommittedSet keeps the fixtures usable: a hand-edited case with
// a broken URL or a truncated capture is caught by `make check`, not by a paid
// scoring run.
func TestLoadOfTheCommittedSet(t *testing.T) {
	cases, err := Load("testdata/saves")
	if err != nil {
		t.Fatalf("the committed scored set does not load: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("the committed scored set is empty")
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.ID] {
			t.Errorf("duplicate id %q", c.ID)
		}
		seen[c.ID] = true
		if !strings.HasPrefix(c.URL, "http") {
			t.Errorf("%s: url = %q", c.ID, c.URL)
		}
		if strings.TrimSpace(c.Text) == "" {
			t.Errorf("%s: no page text", c.ID)
		}
		if c.Kind != "" && !c.Kind.Valid() {
			t.Errorf("%s: kind = %q", c.ID, c.Kind)
		}
	}
}

// --- recording a set --------------------------------------------------------

func TestParseSources(t *testing.T) {
	sources, err := ParseSources(strings.NewReader(`
# a comment
pancakes https://kitchen.example/pancakes
chain   https://example.test/chain
`))
	if err != nil {
		t.Fatalf("ParseSources: %v", err)
	}
	if len(sources) != 2 || sources[0].ID != "pancakes" || sources[1].URL != "https://example.test/chain" {
		t.Fatalf("sources = %+v", sources)
	}
}

func TestParseSourcesRejectsADuplicateID(t *testing.T) {
	_, err := ParseSources(strings.NewReader("a https://example.test/1\na https://example.test/2\n"))
	if err == nil || !strings.Contains(err.Error(), "a") {
		t.Fatalf("ParseSources = %v, want a duplicate complaint", err)
	}
}

func TestParseSourcesRejectsTheLabelsID(t *testing.T) {
	// A capture called labels would be written to labels.json and destroy the
	// set's own labels.
	_, err := ParseSources(strings.NewReader("labels https://example.test/x\n"))
	if err == nil || !strings.Contains(err.Error(), "labels") {
		t.Fatalf("ParseSources = %v, want a reserved-id complaint", err)
	}
}

func TestParseSourcesRejectsAnIDThatIsNotAFilename(t *testing.T) {
	_, err := ParseSources(strings.NewReader("../../etc/passwd https://example.test/x\n"))
	if err == nil {
		t.Fatal("a path-like id must be refused: it becomes a filename")
	}
}

type fakeFetcher struct {
	pages       map[string]string
	contentType string
	err         error
}

func (f *fakeFetcher) Fetch(_ context.Context, rawURL string) (*enrich.FetchedPage, error) {
	if f.err != nil {
		return nil, f.err
	}
	body, ok := f.pages[rawURL]
	if !ok {
		return nil, errors.New("no fixture for " + rawURL)
	}
	contentType := f.contentType
	if contentType == "" {
		contentType = "text/html; charset=utf-8"
	}
	return &enrich.FetchedPage{Body: strings.NewReader(body), ContentType: contentType}, nil
}

func TestRecordWritesOneCaseFilePerSource(t *testing.T) {
	dir := t.TempDir()
	fetcher := &fakeFetcher{pages: map[string]string{
		"https://kitchen.example/pancakes": `<html><head><title>Sourdough pancakes</title></head>
<body><article><h1>Sourdough pancakes</h1><p>Whisk the starter, egg and milk. Rest, then cook.</p></article></body></html>`,
	}}
	failures, err := Record(context.Background(), fetcher, dir, []Source{
		{ID: "pancakes", URL: "https://kitchen.example/pancakes"},
		{ID: "gone", URL: "https://example.test/gone"},
	}, func() time.Time { return time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(failures) != 1 || failures[0].ID != "gone" {
		t.Fatalf("failures = %+v, want only \"gone\"", failures)
	}

	cases, err := Load(dir)
	if err != nil {
		t.Fatalf("Load after Record: %v", err)
	}
	if len(cases) != 1 || cases[0].ID != "pancakes" {
		t.Fatalf("cases = %+v", cases)
	}
	got := cases[0]
	if !strings.Contains(got.Title, "Sourdough") || !strings.Contains(got.Text, "Whisk") {
		t.Errorf("recorded page is empty: %+v", got)
	}
	if got.Captured != "2026-11-22" {
		t.Errorf("captured = %q", got.Captured)
	}
	if got.Kind != "" {
		t.Errorf("record must leave the label to a human, got kind %q", got.Kind)
	}
}

func TestRecordDoesNotTouchTheLabels(t *testing.T) {
	dir := writeSet(t, map[string]string{"labels.json": `{"pancakes": {"kind": "recipe"}}`})
	fetcher := &fakeFetcher{pages: map[string]string{
		"https://kitchen.example/pancakes": `<html><head><title>P</title></head><body><p>Whisk.</p></body></html>`,
	}}
	if _, err := Record(context.Background(), fetcher, dir, []Source{
		{ID: "pancakes", URL: "https://kitchen.example/pancakes"},
	}, time.Now); err != nil {
		t.Fatalf("Record: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "labels.json"))
	if err != nil {
		t.Fatalf("labels.json: %v", err)
	}
	if !strings.Contains(string(raw), "recipe") {
		t.Errorf("re-recording clobbered the labels: %s", raw)
	}
}

func TestRecordRefusesANonHTMLPage(t *testing.T) {
	// Production fails a PDF before extraction (until phase 6), so a capture of
	// one could never be a scored save.
	dir := t.TempDir()
	fetcher := &fakeFetcher{
		pages:       map[string]string{"https://example.test/paper.pdf": "%PDF-1.7"},
		contentType: "application/pdf",
	}
	failures, err := Record(context.Background(), fetcher, dir, []Source{
		{ID: "paper", URL: "https://example.test/paper.pdf"},
	}, time.Now)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(failures) != 1 || !strings.Contains(failures[0].Err.Error(), "application/pdf") {
		t.Fatalf("failures = %+v, want the content type named", failures)
	}
	if _, err := os.Stat(filepath.Join(dir, "paper.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a non-HTML page was captured: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, labelsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed capture was written into the labels: %v", err)
	}
}

func TestLoadVocabulary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vocabulary.txt")
	body := "# the library's canonical tags\nGo\n\ngo\nrust\n  sqlite  \n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tags, err := LoadVocabulary(path)
	if err != nil {
		t.Fatalf("LoadVocabulary: %v", err)
	}
	if len(tags) != 3 || tags[0] != "go" || tags[2] != "sqlite" {
		t.Errorf("tags = %v, want go, rust, sqlite", tags)
	}
}

func TestLoadVocabularyCapsAtThePipelineLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vocabulary.txt")
	var b strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "tag%02d\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	tags, err := LoadVocabulary(path)
	if err != nil {
		t.Fatalf("LoadVocabulary: %v", err)
	}
	if len(tags) != 50 {
		t.Errorf("tags = %d, want the pipeline's cap of 50", len(tags))
	}
}

func TestRecordNamesEveryNewCaseInTheLabels(t *testing.T) {
	dir := t.TempDir()
	fetcher := &fakeFetcher{pages: map[string]string{
		"https://kitchen.example/pancakes": `<html><head><title>P</title></head><body><p>Whisk.</p></body></html>`,
	}}
	if _, err := Record(context.Background(), fetcher, dir, []Source{
		{ID: "pancakes", URL: "https://kitchen.example/pancakes"},
	}, time.Now); err != nil {
		t.Fatalf("Record: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, labelsFile))
	if err != nil {
		t.Fatalf("labels.json: %v", err)
	}
	if !strings.Contains(string(raw), `"pancakes": {"kind": "", "tags": []}`) {
		t.Errorf("labels.json has no blank entry for the new case:\n%s", raw)
	}
}

func TestRecordReplacesAnEarlierCapture(t *testing.T) {
	dir := writeSet(t, map[string]string{"pancakes.json": caseJSON})
	fetcher := &fakeFetcher{pages: map[string]string{
		"https://kitchen.example/pancakes": `<html><head><title>New</title></head><body><p>Whisk again.</p></body></html>`,
	}}
	if _, err := Record(context.Background(), fetcher, dir, []Source{
		{ID: "pancakes", URL: "https://kitchen.example/pancakes"},
	}, time.Now); err != nil {
		t.Fatalf("Record: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "pancakes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "A use for the discard") || !strings.Contains(string(raw), "Whisk again") {
		t.Errorf("the capture was not replaced: %s", raw)
	}
}
