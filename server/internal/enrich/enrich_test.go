package enrich

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

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

// If the metadata endpoint cannot answer either, the page's own failure is what
// the row keeps.
func TestProcessKeepsThePageFailureWhenMetadataFails(t *testing.T) {
	s := openStore(t)
	en := newTestEnricher(t, s, blockedFetcher(), &fakeDecider{}, &fakeWriter{}, &fakeEmbedder{})
	en.Videos = &fakeVideos{err: errors.New("oEmbed answered 404")}

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
	if after.State == model.StateReady {
		t.Errorf("state = %q, want it to stay a failure", after.State)
	}
	if !strings.Contains(after.LastError, "blocked") && !strings.Contains(after.LastError, "sorry") {
		t.Errorf("last_error = %q, want the page's reason, not the metadata's", after.LastError)
	}
}
