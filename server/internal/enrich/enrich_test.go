package enrich

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/AutumnsGrove/Harbor/server/internal/fetch"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/youtube"
)

// plans/03 tests 6–10 and 14–18 at the pipeline level. The fetch, decide,
// chat and embed calls are hand-written fakes with recorded answers; the store
// is real SQLite, because the transaction is part of what is under test.

const page = `<html><head><title>Pancakes</title>
<meta property="og:site_name" content="Kitchen">
<meta property="og:description" content="A use for the discard.">
</head><body><article><h1>Pancakes</h1>
<p>Whisk the starter, egg and milk. Rest, then cook in a hot pan.</p>
</article></body></html>`

var fixedNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

type fakeFetcher struct {
	body        string
	contentType string
	final       *url.URL
	err         error
	calls       int
	lastURL     string
}

func (f *fakeFetcher) Fetch(_ context.Context, rawURL string) (*FetchedPage, error) {
	f.calls++
	f.lastURL = rawURL
	if f.err != nil {
		return nil, f.err
	}
	contentType := f.contentType
	if contentType == "" {
		contentType = "text/html; charset=utf-8"
	}
	return &FetchedPage{Body: strings.NewReader(f.body), URL: f.final, ContentType: contentType}, nil
}

type fakeDecider struct {
	result Classification
	err    error
	calls  int
	last   ClassifyInput
}

func (f *fakeDecider) Decide(_ context.Context, input ClassifyInput) (Classification, error) {
	f.calls++
	f.last = input
	return f.result, f.err
}

type fakeWriter struct {
	result Content
	err    error
	calls  int
	last   ContentInput
}

func (f *fakeWriter) Write(_ context.Context, input ContentInput) (Content, error) {
	f.calls++
	f.last = input
	return f.result, f.err
}

type fakeEmbedder struct {
	vec   []float32
	err   error
	calls int
	last  string
}

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	f.calls++
	f.last = text
	if f.err != nil {
		return nil, f.err
	}
	if f.vec != nil {
		return f.vec, nil
	}
	vec := make([]float32, EmbeddingDims)
	vec[0] = 0.5
	return vec, nil
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

func createItem(t *testing.T, s *store.Store, in store.ItemInput) model.Item {
	t.Helper()
	item, err := s.CreateItem(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	return item
}

func newTestEnricher(t *testing.T, s Store, f *fakeFetcher, d *fakeDecider, w *fakeWriter, em *fakeEmbedder) *Enricher {
	t.Helper()
	en := New(f, d, w, em, s, nil)
	// Production sets these from the adapter's constants; tests use the
	// pipeline's own current versions.
	en.PromptVersion = DefaultPromptVersion
	en.EmbeddingModel = "perplexity/pplx-embed-v1-0.6b"
	en.Now = func() time.Time { return fixedNow }
	return en
}

func happyFakes() (*fakeFetcher, *fakeDecider, *fakeWriter, *fakeEmbedder) {
	final, _ := url.Parse("https://kitchen.example/pancakes")
	return &fakeFetcher{body: page, final: final},
		&fakeDecider{result: Classification{Kind: model.KindRecipe}},
		&fakeWriter{result: Content{
			Title:   "Sourdough pancakes",
			Summary: "Thin, tangy pancakes made from starter discard.",
			Tags:    []string{"Baking", "bread", "bread", " Breakfast "},
		}},
		&fakeEmbedder{}
}

func TestProcessEnrichesALink(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	// A vocabulary to hand the prompt.
	seed := createItem(t, s, store.ItemInput{ID: "seed", Note: "x", Tags: []string{"rust"}})
	if _, err := s.SetItemTags(ctx, seed.ID, []string{"rust"}, model.TagSourceAI); err != nil {
		t.Fatal(err)
	}
	item := createItem(t, s, store.ItemInput{
		ID: "a", URL: ptr("https://kitchen.example/pancakes"), Title: "https://kitchen.example/pancakes",
	})

	f, d, w, em := happyFakes()
	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if f.calls != 1 || f.lastURL != "https://kitchen.example/pancakes" {
		t.Errorf("fetch calls = %d, last = %q", f.calls, f.lastURL)
	}
	if d.calls != 1 || !strings.Contains(d.last.Text, "Whisk") {
		t.Errorf("decide input = %+v", d.last)
	}
	if w.calls != 1 || !strings.Contains(strings.Join(w.last.Vocabulary, ","), "rust") {
		t.Errorf("writer input vocabulary = %v", w.last.Vocabulary)
	}

	enriched, err := s.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if enriched.Kind != model.KindRecipe || enriched.KindSource != model.KindSourceAI {
		t.Errorf("kind = %q/%q, want recipe/ai", enriched.Kind, enriched.KindSource)
	}
	if enriched.Title != "Sourdough pancakes" {
		t.Errorf("title = %q, want the cleaned title", enriched.Title)
	}
	if enriched.Summary != "Thin, tangy pancakes made from starter discard." {
		t.Errorf("summary = %q", enriched.Summary)
	}
	if !strings.Contains(enriched.ContentText, "Whisk") || !strings.Contains(enriched.ContentMD, "Whisk") {
		t.Errorf("text/markdown not written: %q / %q", enriched.ContentText, enriched.ContentMD)
	}
	if enriched.DetailsJSON != "{}" {
		t.Errorf("details_json = %q, want empty for a page with no JSON-LD", enriched.DetailsJSON)
	}
	if !strings.Contains(enriched.OGJSON, "Kitchen") {
		t.Errorf("og_json = %q, want the page's OG metadata", enriched.OGJSON)
	}
	if enriched.State != model.StateReady || enriched.EnrichedAt == nil || enriched.Attempts != 0 {
		t.Errorf("state = %q, enrichedAt = %v, attempts = %d", enriched.State, enriched.EnrichedAt, enriched.Attempts)
	}
	if enriched.PromptVersion != DefaultPromptVersion || enriched.ExtractVersion != DefaultExtractVersion {
		t.Errorf("versions = %q/%q", enriched.PromptVersion, enriched.ExtractVersion)
	}

	tags, err := s.TagsForItem(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 3 || tags[0] != "baking" || tags[1] != "bread" || tags[2] != "breakfast" {
		t.Errorf("tags = %v, want normalised and deduplicated", tags)
	}

	modelName, vec, hash, err := s.GetEmbedding(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetEmbedding: %v", err)
	}
	if modelName != "perplexity/pplx-embed-v1-0.6b" || len(vec) != EmbeddingDims {
		t.Errorf("embedding = %q, %d dims", modelName, len(vec))
	}
	if hash != hashOf(em.last) {
		t.Errorf("stored hash does not match the embedding input")
	}
	if !strings.Contains(em.last, "Sourdough pancakes") || !strings.Contains(em.last, "Thin, tangy") {
		t.Errorf("embedding input = %q, want title and summary", em.last)
	}
}

// plans/03 test 18: a typed note makes no fetch and no decide call.
func TestProcessANoteSkipsFetchAndClassify(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{ID: "a", Note: "A thought about sourdough."})
	f, d, w, em := happyFakes()
	w.result = Content{Summary: "A note about bread.", Tags: []string{"baking"}}

	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if f.calls != 0 {
		t.Errorf("fetch calls = %d for a note, want 0", f.calls)
	}
	if d.calls != 0 {
		t.Errorf("decide calls = %d for a note, want 0", d.calls)
	}
	if w.calls != 1 || em.calls != 1 {
		t.Errorf("writer/embed calls = %d/%d, want 1/1", w.calls, em.calls)
	}
	enriched, _ := s.GetItem(ctx, item.ID)
	if enriched.Kind != model.KindNote {
		t.Errorf("kind = %q, want note", enriched.Kind)
	}
	if enriched.ContentText != "A thought about sourdough." {
		t.Errorf("content_text = %q, want the note", enriched.ContentText)
	}
}

func TestProcessCoercesAnUnknownKind(t *testing.T) {
	s := openStore(t)
	item := createItem(t, s, store.ItemInput{ID: "a", URL: ptr("https://example.com/a")})
	f, d, w, em := happyFakes()
	d.result = Classification{Kind: model.Kind("listicle")}

	if err := newTestEnricher(t, s, f, d, w, em).Process(context.Background(), item); err != nil {
		t.Fatalf("Process: %v", err)
	}
	enriched, _ := s.GetItem(context.Background(), item.ID)
	if enriched.Kind != model.KindArticle {
		t.Errorf("kind = %q, want article for a kind outside the twelve", enriched.Kind)
	}
}

func TestProcessNeverCallsDecideForAManualKind(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{ID: "a", URL: ptr("https://example.com/a")})
	video := model.KindVideo
	if _, err := s.UpdateItem(ctx, item.ID, store.ItemPatch{Kind: &video}); err != nil {
		t.Fatal(err)
	}
	item, _ = s.GetItem(ctx, item.ID)

	f, d, w, em := happyFakes()
	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if d.calls != 0 {
		t.Errorf("decide calls = %d for a manual kind, want 0", d.calls)
	}
	enriched, _ := s.GetItem(ctx, item.ID)
	if enriched.Kind != model.KindVideo || enriched.KindSource != model.KindSourceManual {
		t.Errorf("kind = %q/%q, want video/manual", enriched.Kind, enriched.KindSource)
	}
}

func TestProcessFailsFastOnAPermanentFetchError(t *testing.T) {
	s := openStore(t)
	item := createItem(t, s, store.ItemInput{ID: "a", URL: ptr("https://example.com/gone")})
	f, d, w, em := happyFakes()
	f.err = &fetch.HTTPError{URL: "https://example.com/gone", Status: 404}

	if err := newTestEnricher(t, s, f, d, w, em).Process(context.Background(), item); err == nil {
		t.Fatal("Process accepted a 404")
	}
	enriched, _ := s.GetItem(context.Background(), item.ID)
	if enriched.State != model.StateFailed || enriched.Attempts != 1 || enriched.NextAttemptAt != nil {
		t.Errorf("after a 404 = state %q, attempts %d, next %v; want terminal", enriched.State, enriched.Attempts, enriched.NextAttemptAt)
	}
	if !strings.Contains(enriched.LastError, "404") {
		t.Errorf("last_error = %q", enriched.LastError)
	}
	if w.calls != 0 || em.calls != 0 {
		t.Errorf("a terminal fetch still wrote: writer %d, embed %d", w.calls, em.calls)
	}
}

func TestProcessSchedulesARetryForA503(t *testing.T) {
	s := openStore(t)
	item := createItem(t, s, store.ItemInput{ID: "a", URL: ptr("https://example.com/a")})
	f, d, w, em := happyFakes()
	f.err = &fetch.HTTPError{URL: "https://example.com/a", Status: 503}

	if err := newTestEnricher(t, s, f, d, w, em).Process(context.Background(), item); err == nil {
		t.Fatal("Process accepted a 503")
	}
	enriched, _ := s.GetItem(context.Background(), item.ID)
	if enriched.State != model.StateQueued || enriched.Attempts != 1 || enriched.NextAttemptAt == nil {
		t.Fatalf("after a 503 = state %q, attempts %d, next %v", enriched.State, enriched.Attempts, enriched.NextAttemptAt)
	}
	if want := fixedNow.Add(15 * time.Minute); !enriched.NextAttemptAt.Equal(want) {
		t.Errorf("next attempt = %v, want %v", enriched.NextAttemptAt, want)
	}
}

func TestTheThirdFailureParksTheItem(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{ID: "a", URL: ptr("https://example.com/a")})
	if _, err := s.RecordEnrichmentFailure(ctx, item.ID, "first", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordEnrichmentFailure(ctx, item.ID, "second", nil); err != nil {
		t.Fatal(err)
	}
	item, _ = s.GetItem(ctx, item.ID)
	if item.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", item.Attempts)
	}

	f, d, w, em := happyFakes()
	f.err = &fetch.HTTPError{URL: "https://example.com/a", Status: 503}
	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err == nil {
		t.Fatal("Process accepted a third failure")
	}
	enriched, _ := s.GetItem(ctx, item.ID)
	if enriched.Attempts != MaxAttempts || enriched.State != model.StateFailed || enriched.NextAttemptAt != nil {
		t.Errorf("third failure = attempts %d, state %q, next %v; want terminal", enriched.Attempts, enriched.State, enriched.NextAttemptAt)
	}
}

func TestTooManyTagsIsARetryNotAPartialWrite(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{ID: "a", URL: ptr("https://example.com/a")})
	f, d, w, em := happyFakes()
	w.result.Tags = make([]string, 31)
	for i := range w.result.Tags {
		w.result.Tags[i] = "tag" + string(rune('a'+i))
	}

	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err == nil {
		t.Fatal("31 tags were accepted")
	}
	enriched, _ := s.GetItem(ctx, item.ID)
	if enriched.Summary != "" || enriched.ContentText != "" {
		t.Errorf("a failed pass wrote a partial row: %+v", enriched)
	}
	if enriched.Attempts != 1 || enriched.State != model.StateQueued {
		t.Errorf("state = %q, attempts = %d; want a queued retry", enriched.State, enriched.Attempts)
	}
}

func TestAWrongSizedEmbeddingFailsTheJob(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{ID: "a", URL: ptr("https://example.com/a")})
	f, d, w, em := happyFakes()
	em.vec = make([]float32, 100)

	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err == nil {
		t.Fatal("a 100-dimension vector was accepted")
	}
	if _, _, _, err := s.GetEmbedding(ctx, item.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a bad vector was stored: %v", err)
	}
	enriched, _ := s.GetItem(ctx, item.ID)
	if enriched.State != model.StateFailed || enriched.Attempts != 1 {
		t.Errorf("state = %q, attempts = %d; want a terminal failure", enriched.State, enriched.Attempts)
	}
}

func TestProcessSkipsAnUnchangedReadyItem(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{ID: "a", Note: "x"})
	enriched, err := s.ApplyEnrichment(ctx, item.ID, store.Enrichment{
		Kind: model.KindNote, KindSource: model.KindSourceAI,
		Summary: "done", PromptVersion: DefaultPromptVersion, ExtractVersion: DefaultExtractVersion,
	})
	if err != nil {
		t.Fatal(err)
	}

	f, d, w, em := happyFakes()
	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, enriched); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if f.calls+d.calls+w.calls+em.calls != 0 {
		t.Errorf("an unchanged ready item made calls: fetch %d, decide %d, write %d, embed %d",
			f.calls, d.calls, w.calls, em.calls)
	}
}

func TestPromptDriftReenriches(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{ID: "a", Note: "x"})
	enriched, err := s.ApplyEnrichment(ctx, item.ID, store.Enrichment{
		Kind: model.KindNote, KindSource: model.KindSourceAI,
		Summary: "old prompt", PromptVersion: "tag.v0", ExtractVersion: DefaultExtractVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	f, d, w, em := happyFakes()
	w.result = Content{Summary: "new prompt", Tags: []string{"go"}}
	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, enriched); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if w.calls != 1 {
		t.Errorf("writer calls = %d, want the item re-enriched", w.calls)
	}
	after, _ := s.GetItem(ctx, item.ID)
	if after.Summary != "new prompt" || after.PromptVersion != DefaultPromptVersion {
		t.Errorf("after re-enrichment = %q / %q", after.Summary, after.PromptVersion)
	}
}

func TestRetryDelayAddsUpToAboutAnHour(t *testing.T) {
	if got := RetryDelay(1); got != 15*time.Minute {
		t.Errorf("RetryDelay(1) = %v", got)
	}
	if got := RetryDelay(2); got != 45*time.Minute {
		t.Errorf("RetryDelay(2) = %v", got)
	}
	if total := RetryDelay(1) + RetryDelay(2); total != time.Hour {
		t.Errorf("the retry budget is %v, want about an hour", total)
	}
}

func TestNonHTMLIsTerminal(t *testing.T) {
	s := openStore(t)
	item := createItem(t, s, store.ItemInput{ID: "a", URL: ptr("https://example.com/a.pdf")})
	f, d, w, em := happyFakes()
	f.contentType = "application/pdf"

	if err := newTestEnricher(t, s, f, d, w, em).Process(context.Background(), item); err == nil {
		t.Fatal("a PDF was accepted by the HTML extractor")
	}
	enriched, _ := s.GetItem(context.Background(), item.ID)
	if enriched.State != model.StateFailed || enriched.NextAttemptAt != nil {
		t.Errorf("PDF failure = state %q, next %v; want terminal until phase 6", enriched.State, enriched.NextAttemptAt)
	}
}

// A pipeline built without the adapter's constants must fail visibly rather
// than store a vector under a guessed model name.
func TestAnUnconfiguredPipelineFailsLoudly(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{ID: "a", Note: "x"})
	f, d, w, em := happyFakes()
	unconfigured := New(f, d, w, em, s, nil)

	if err := unconfigured.Process(ctx, item); err == nil {
		t.Fatal("an unconfigured pipeline accepted an item")
	}
	after, _ := s.GetItem(ctx, item.ID)
	if after.State != model.StateFailed || !strings.Contains(after.LastError, "configured") {
		t.Errorf("state = %q, last_error = %q; want a visible configuration failure", after.State, after.LastError)
	}
	if w.calls != 0 || em.calls != 0 {
		t.Errorf("an unconfigured pipeline made calls: write %d, embed %d", w.calls, em.calls)
	}
}

// --- the video fallback -----------------------------------------------------
//
// Spike S8 and the potato's first real drain: 371 of 421 YouTube saves failed
// with Google's /sorry/ wall. A video whose page cannot be read is still a video
// with a title, a channel and a still, and the item should be filed from those
// rather than parked as a failure.

type fakeVideos struct {
	meta  youtube.Metadata
	err   error
	calls int
	last  string
}

func (f *fakeVideos) Metadata(_ context.Context, rawURL string) (youtube.Metadata, error) {
	f.calls++
	f.last = rawURL
	if f.err != nil {
		return youtube.Metadata{}, f.err
	}
	return f.meta, nil
}

func blockedFetcher() *fakeFetcher {
	return &fakeFetcher{err: fmt.Errorf("%w: https://www.google.com/sorry/index", fetch.ErrBlocked)}
}

// The wall costs the title nothing, and it must not cost a model call: there is
// no text to summarise and phase 7 will redo the item with a transcript.
func TestProcessFilesAWalledVideoFromPublicMetadata(t *testing.T) {
	s := openStore(t)
	f, d, w, em := blockedFetcher(), &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{}
	en := newTestEnricher(t, s, f, d, w, em)
	videos := &fakeVideos{meta: youtube.Metadata{
		VideoID:   "dQw4w9WgXcQ",
		Title:     "Never Gonna Give You Up",
		Author:    "Rick Astley",
		Thumbnail: "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg",
	}}
	en.Videos = videos

	item := createItem(t, s, store.ItemInput{
		ID: "video", URL: ptr("https://www.youtube.com/watch?v=dQw4w9WgXcQ"),
		Title: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Source: model.SourceImport,
	})
	if err := en.Process(context.Background(), item); err != nil {
		t.Fatalf("Process: %v", err)
	}

	after, err := s.GetItem(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if after.State != model.StateReady {
		t.Errorf("state = %q, want ready: the item is usable", after.State)
	}
	if after.Kind != model.KindVideo || after.KindSource != model.KindSourceDefault {
		t.Errorf("kind = %q/%q, want video from the URL", after.Kind, after.KindSource)
	}
	if after.Title != "Never Gonna Give You Up" {
		t.Errorf("title = %q, want the video's own", after.Title)
	}
	if after.PromptVersion != VideoMetadataVersion {
		t.Errorf("prompt version = %q, want %q: no prompt ran", after.PromptVersion, VideoMetadataVersion)
	}
	if !strings.Contains(after.ExtractError, "could not be read") {
		t.Errorf("extract_error = %q, want the page's failure kept", after.ExtractError)
	}
	if !strings.Contains(after.DetailsJSON, `"author":["Rick Astley"]`) ||
		!strings.Contains(after.DetailsJSON, "hqdefault.jpg") ||
		!strings.Contains(after.DetailsJSON, `"type":"VideoObject"`) {
		t.Errorf("details = %s, want the channel, the still and the type", after.DetailsJSON)
	}
	if after.Summary != "" {
		t.Errorf("summary = %q, want none: no model read this item", after.Summary)
	}

	// The point of the fallback: it costs nothing.
	if d.calls+w.calls+em.calls != 0 {
		t.Errorf("model calls = decide %d, write %d, embed %d; want none", d.calls, w.calls, em.calls)
	}
	if tags, err := s.TagsForItem(context.Background(), item.ID); err != nil || len(tags) != 0 {
		t.Errorf("tags = %v, %v; want none invented", tags, err)
	}
}

// A walled page that still parses to nothing is the same outcome as a walled
// page: there is nothing to summarise.
func TestProcessFilesAVideoWhosePageHasNoText(t *testing.T) {
	s := openStore(t)
	f := &fakeFetcher{body: `<html><head><title>YouTube</title></head><body></body></html>`}
	en := newTestEnricher(t, s, f, &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{})
	en.Videos = &fakeVideos{meta: youtube.Metadata{VideoID: "dQw4w9WgXcQ", Title: "A video"}}

	item := createItem(t, s, store.ItemInput{
		ID: "empty", URL: ptr("https://youtu.be/dQw4w9WgXcQ"), Source: model.SourceImport,
	})
	if err := en.Process(context.Background(), item); err != nil {
		t.Fatalf("Process: %v", err)
	}
	after, err := s.GetItem(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if after.State != model.StateReady || after.Title != "A video" {
		t.Errorf("item = %q %q, want it ready with the video's title", after.State, after.Title)
	}
}

// A video with no page text and no metadata has nothing to read: it must not be
// handed to the model as an empty string, and the row keeps the page's reason.
func TestProcessDoesNotSendAnEmptyVideoToTheModel(t *testing.T) {
	s := openStore(t)
	f := &fakeFetcher{body: `<html><body></body></html>`}
	d, w, em := &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{}
	en := newTestEnricher(t, s, f, d, w, em)
	en.Videos = &fakeVideos{err: errors.New("oEmbed answered 404")}

	item := createItem(t, s, store.ItemInput{
		ID: "empty-video", URL: ptr("https://www.youtube.com/watch?v=dQw4w9WgXcQ"), Source: model.SourceImport,
	})
	if err := en.Process(context.Background(), item); err == nil {
		t.Fatal("a video with nothing to read was reported as enriched")
	}
	if d.calls+w.calls+em.calls != 0 {
		t.Errorf("model calls = decide %d, write %d, embed %d; want none", d.calls, w.calls, em.calls)
	}
	after, err := s.GetItem(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	// Which of the two reasons is kept depends on the endpoint: its own when it
	// answered, the page's when it did not (see the two tests above). What must
	// never happen is a model call on an empty string.
	if after.State == model.StateReady {
		t.Errorf("state = %q, want the item left for a retry", after.State)
	}
	if after.LastError == "" {
		t.Error("last_error is empty; the item does not say why it is not enriched")
	}
}

// Everything else behaves exactly as before: a non-video failure is a failure,
// and the video endpoint is not even asked.
func TestProcessKeepsTheFailureForANonVideo(t *testing.T) {
	s := openStore(t)
	en := newTestEnricher(t, s, blockedFetcher(), &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{})
	videos := &fakeVideos{}
	en.Videos = videos

	item := createItem(t, s, store.ItemInput{
		ID: "article", URL: ptr("https://example.com/walled"), Source: model.SourceImport,
	})
	if err := en.Process(context.Background(), item); err == nil {
		t.Fatal("a walled article was reported as enriched")
	}
	if videos.calls != 0 {
		t.Errorf("the video endpoint was asked %d times for an article", videos.calls)
	}
}

// A definitive answer from the endpoint — this id has nothing — parks the item:
// no retry will change a deleted video.
func TestProcessFailsAVideoTheEndpointHasNoMetadataFor(t *testing.T) {
	s := openStore(t)
	en := newTestEnricher(t, s, blockedFetcher(), &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{})
	en.Videos = &fakeVideos{err: fmt.Errorf("%w: oEmbed answered 404", youtube.ErrNoMetadata)}

	item := createItem(t, s, store.ItemInput{
		ID: "gone", URL: ptr("https://www.youtube.com/watch?v=dQw4w9WgXcQ"), Source: model.SourceImport,
	})
	if err := en.Process(context.Background(), item); err == nil {
		t.Fatal("a video with no metadata was reported as enriched")
	}
	after, err := s.GetItem(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if after.State != model.StateFailed {
		t.Errorf("state = %q, want failed: the endpoint answered definitively", after.State)
	}
	if after.NextAttemptAt != nil {
		t.Errorf("next attempt = %v, want none", after.NextAttemptAt)
	}
	if !strings.Contains(after.LastError, "404") {
		t.Errorf("last_error = %q, want the reason", after.LastError)
	}
}

// A transport error or a rate limit is the endpoint being busy, not the video
// being gone — that keeps the retry schedule, on every hook. A burst of 371
// retries against a keyless endpoint is exactly when a 429 happens.
func TestProcessRetriesWhenTheMetadataEndpointIsBusy(t *testing.T) {
	tests := []struct {
		name string
		body string // page the fetcher returns; "" means the fetch itself fails
	}{
		{"the page was blocked", ""},
		{"the page carried no text", `<html><body></body></html>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := openStore(t)
			fetcher := blockedFetcher()
			if tc.body != "" {
				fetcher = &fakeFetcher{body: tc.body}
			}
			en := newTestEnricher(t, s, fetcher, &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{})
			en.Videos = &fakeVideos{err: errors.New("youtube: oEmbed: context deadline exceeded")}

			item := createItem(t, s, store.ItemInput{
				ID: "busy", URL: ptr("https://www.youtube.com/watch?v=dQw4w9WgXcQ"), Source: model.SourceImport,
			})
			if err := en.Process(context.Background(), item); err == nil {
				t.Fatal("a busy endpoint was reported as enriched")
			}
			after, err := s.GetItem(context.Background(), item.ID)
			if err != nil {
				t.Fatalf("GetItem: %v", err)
			}
			if after.State != model.StateQueued {
				t.Errorf("state = %q, want queued: the endpoint was busy, not definitive", after.State)
			}
			if after.NextAttemptAt == nil {
				t.Error("next_attempt_at is nil; the row will never be tried again")
			}
		})
	}
}

// A Markdown page is read as a document rather than refused: the body becomes
// text and Markdown, its front matter becomes the cover and the date, and the
// item reaches ready (issue #73).
func TestProcessEnrichesAMarkdownPage(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{
		ID: "a", URL: ptr("https://gwern.net/blog/2026/make-me-care"), Title: "First, Make Me Care",
	})

	f, d, w, em := happyFakes()
	final, _ := url.Parse("https://gwern.net/blog/2026/make-me-care")
	f.final = final
	f.contentType = "text/markdown; charset=utf-8"
	f.body = "---\ntitle: 'First, Make Me Care'\ndescription: \"Writing advice about hooks.\"\nthumbnail: /img/thinkingface.jpg\ncreated: 2026-01-14\n---\n\nVenice built a maritime empire from a city that could not feed itself.\n"

	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err != nil {
		t.Fatalf("Process: %v", err)
	}

	enriched, err := s.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if enriched.State != model.StateReady {
		t.Fatalf("state = %q (%s), want ready", enriched.State, enriched.LastError)
	}
	if !strings.Contains(enriched.ContentText, "maritime empire") {
		t.Errorf("content_text = %q, want the document body", enriched.ContentText)
	}
	if !strings.Contains(enriched.OGJSON, "https://gwern.net/img/thinkingface.jpg") {
		t.Errorf("og_json = %q, want the cover resolved against the page", enriched.OGJSON)
	}
	if d.calls != 1 || !strings.Contains(d.last.Text, "maritime empire") {
		t.Errorf("decide input = %+v, want the document body", d.last)
	}
	if w.calls != 1 || !strings.Contains(w.last.Text, "maritime empire") {
		t.Errorf("writer input = %+v", w.last)
	}
	if len(enriched.ContentText) == 0 {
		t.Error("no text was stored for a readable document")
	}
}

// A plain text file is a document too; it used to be handed to the HTML parser.
func TestProcessEnrichesAPlainTextPage(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	item := createItem(t, s, store.ItemInput{
		ID: "a", URL: ptr("https://example.test/notes.txt"), Title: "notes.txt",
	})

	f, d, w, em := happyFakes()
	f.contentType = "text/plain; charset=utf-8"
	f.body = "A plain text file, not Markdown.\n\nIt has paragraphs and no metadata.\n"

	if err := newTestEnricher(t, s, f, d, w, em).Process(ctx, item); err != nil {
		t.Fatalf("Process: %v", err)
	}
	enriched, _ := s.GetItem(ctx, item.ID)
	if enriched.State != model.StateReady {
		t.Fatalf("state = %q (%s), want ready", enriched.State, enriched.LastError)
	}
	if !strings.Contains(enriched.ContentText, "It has paragraphs") {
		t.Errorf("content_text = %q", enriched.ContentText)
	}
	if enriched.OGJSON != "" && strings.Contains(enriched.OGJSON, "image") {
		t.Errorf("og_json invented a cover for a text file: %q", enriched.OGJSON)
	}
}

// A video whose owner disabled embedding answers 401 or 403 — the video plays
// fine, the endpoint just will not describe it. Retiring the item for that loses
// a real save with a real URL, so it is filed ready from the title it already has
// (issue #79).
func TestProcessFilesARefusedVideoFromItsSavedTitle(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	const savedTitle = "Medieval Cat Laws - Laws for your Medieval Cat - YouTube"

	en := newTestEnricher(t, s, blockedFetcher(), &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{})
	en.Videos = &fakeVideos{err: fmt.Errorf("%w: oEmbed answered 401 for jD3b1s-s9bk", youtube.ErrRefused)}

	item := createItem(t, s, store.ItemInput{
		ID: "refused", URL: ptr("https://m.youtube.com/watch?v=jD3b1s-s9bk&ra=m"),
		Title: savedTitle, Source: model.SourceImport,
	})
	if err := en.Process(ctx, item); err != nil {
		t.Fatalf("Process: %v", err)
	}

	after, err := s.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if after.State != model.StateReady {
		t.Fatalf("state = %q (%s), want ready: the video exists", after.State, after.LastError)
	}
	if after.Kind != model.KindVideo {
		t.Errorf("kind = %q, want video", after.Kind)
	}
	if after.Title != savedTitle {
		t.Errorf("title = %q, want the saved one kept", after.Title)
	}
	if !strings.Contains(after.ExtractError, "refuses") {
		t.Errorf("extract_error = %q, want the refusal and what is missing", after.ExtractError)
	}
	if !strings.Contains(after.ExtractError, "no summary or tags") {
		t.Errorf("extract_error = %q, want it to say what the item does not have", after.ExtractError)
	}
	if after.LastError != "" || after.Attempts != 0 || after.NextAttemptAt != nil {
		t.Errorf("failure columns not cleared: last=%q attempts=%d next=%v", after.LastError, after.Attempts, after.NextAttemptAt)
	}
	if after.PromptVersion != VideoMetadataVersion {
		t.Errorf("prompt version = %q, want %q: no prompt ran", after.PromptVersion, VideoMetadataVersion)
	}
}

// The page can be readable enough to carry an og:title and still have no article.
// A refusal then prefers the page's own title over the saved one, and keeps its
// cover.
func TestProcessFilesARefusedVideoFromItsPageTitle(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	f := &fakeFetcher{body: `<html><head>
		<meta property="og:title" content="Medieval Cat Laws">
		<meta property="og:image" content="https://i.ytimg.com/vi/jD3b1s-s9bk/hqdefault.jpg">
	</head><body></body></html>`}

	en := newTestEnricher(t, s, f, &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{})
	en.Videos = &fakeVideos{err: fmt.Errorf("%w: oEmbed answered 403 for jD3b1s-s9bk", youtube.ErrRefused)}

	item := createItem(t, s, store.ItemInput{
		ID: "page", URL: ptr("https://www.youtube.com/watch?v=jD3b1s-s9bk"),
		Title: "https://www.youtube.com/watch?v=jD3b1s-s9bk",
	})
	if err := en.Process(ctx, item); err != nil {
		t.Fatalf("Process: %v", err)
	}
	after, err := s.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if after.State != model.StateReady {
		t.Fatalf("state = %q (%s), want ready", after.State, after.LastError)
	}
	if after.Title != "Medieval Cat Laws" {
		t.Errorf("title = %q, want the page's own", after.Title)
	}
	if !strings.Contains(after.DetailsJSON, "hqdefault.jpg") {
		t.Errorf("details = %s, want the page's cover kept", after.DetailsJSON)
	}
}

// A refusal with nothing to file it under is an honest failure: no title from the
// page, no metadata, and a saved title that is only the URL.
func TestProcessFailsARefusedVideoWithNoTitleToFileItUnder(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	const rawURL = "https://www.youtube.com/watch?v=jD3b1s-s9bk"

	en := newTestEnricher(t, s, blockedFetcher(), &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{})
	en.Videos = &fakeVideos{err: fmt.Errorf("%w: oEmbed answered 401 for jD3b1s-s9bk", youtube.ErrRefused)}

	item := createItem(t, s, store.ItemInput{ID: "bare", URL: ptr(rawURL), Title: rawURL})
	if err := en.Process(ctx, item); err == nil {
		t.Fatal("a refused video with no title was accepted")
	}
	after, err := s.GetItem(ctx, item.ID)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if after.State != model.StateFailed || after.NextAttemptAt != nil {
		t.Errorf("state = %q next = %v, want a terminal failure", after.State, after.NextAttemptAt)
	}
	if !strings.Contains(after.LastError, "refused") || !strings.Contains(after.LastError, "no title") {
		t.Errorf("last_error = %q, want it to say why nothing was filed", after.LastError)
	}
}

// The reason fields are shown in the UI and stored in extract_error, so the bound
// has to be the bound — ellipsis included — and the cut has to land on a rune.
// Review on #89 found this: detail[:max-1] + "…" is three bytes of ellipsis, so
// the result was max+2, and a byte cut can split a multi-byte rune.
func TestShortenIsBoundedAndValidUTF8(t *testing.T) {
	if got := shorten(nil, 120); got != "" {
		t.Errorf("shorten(nil) = %q, want empty", got)
	}
	if got := shorten(errors.New("  short  "), 120); got != "short" {
		t.Errorf("shorten(short) = %q, want it trimmed and unchanged", got)
	}
	// Exactly at the bound is left alone: no ellipsis for a string that fits.
	exact := strings.Repeat("a", 120)
	if got := shorten(errors.New(exact), 120); got != exact {
		t.Errorf("shorten(exact) changed a string that fits: %d bytes", len(got))
	}

	for _, tc := range []struct {
		name string
		body string
		max  int
	}{
		{"ascii over the bound", strings.Repeat("a", 200), 120},
		{"two-byte runes", strings.Repeat("é", 100), 120},
		{"three-byte runes", strings.Repeat("→", 100), 120},
		{"four-byte runes", strings.Repeat("😀", 100), 120},
		{"a tiny bound", strings.Repeat("é", 10), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := shorten(errors.New(tc.body), tc.max)
			if len(got) > tc.max {
				t.Errorf("length = %d, want at most %d: %q", len(got), tc.max, got)
			}
			if !utf8.ValidString(got) {
				t.Errorf("not valid UTF-8: %q", got)
			}
			// A bound too small for the ellipsis cannot say it was cut; it still
			// respects the bound.
			if tc.max > len("…") && !strings.HasSuffix(got, "…") {
				t.Errorf("%q does not say it was cut", got)
			}
		})
	}
}
