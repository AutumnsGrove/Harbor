// Package enrich is the pipeline that turns a queued item into a filed one:
// fetch, extract, classify, tag/summarise, embed, write (design.md §6).
//
// It depends on interfaces, not on Nova or HTTP, so the whole pipeline is
// testable with recorded fixtures and a real database. The Nova adapters live
// beside it and implement these three interfaces.
package enrich

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/extract"
	"github.com/AutumnsGrove/Harbor/server/internal/fetch"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/youtube"
)

// Version constants. A change to either makes existing items re-enrichable;
// prompt_version is stored per item so the scored set can tell which prompt
// produced a row (design.md §6).
const (
	// VideoMetadataVersion is what a video's row records when the page could not
	// be read and its public metadata was used instead. It is not a prompt, and
	// saying so is the point: phase 7 finds these rows by it and gives them a
	// transcript and a real pass.
	VideoMetadataVersion = "oembed.v1"

	DefaultPromptVersion  = "tag.v1"
	DefaultExtractVersion = "extract.v1"

	// EmbeddingDims is pplx-embed-v1-0.6b's dimension (spike S5). A different
	// model means changing this deliberately, not silently accepting a vector
	// of the wrong size.
	EmbeddingDims = 1024

	// MaxAttempts is three tries in total, matching the "could not be enriched
	// after three tries" copy in the attention strip.
	MaxAttempts = 3

	// Input caps. The embedding text is title + summary + the first 2,000
	// characters of the body: enough to place the item next to its neighbours
	// without sending a 2 MB page through an embedding model.
	classifyTextLimit = 4_000
	chatTextLimit     = 8_000
	embedTextLimit    = 2_000

	maxSummaryBytes = 4_000
	maxTitleBytes   = 300
	maxTags         = 8
)

// FetchedPage is what the pipeline needs from a fetch: a seekable body, the
// final URL for relative links, the content type, and a Close that removes the
// temp file. It keeps the pipeline's fakes free of HTTP.
type FetchedPage struct {
	Body        io.ReadSeeker
	URL         *url.URL
	ContentType string
	close       func() error
}

// Close releases the page.
func (p *FetchedPage) Close() error {
	if p.close != nil {
		return p.close()
	}
	if closer, ok := p.Body.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// Fetcher retrieves one page.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string) (*FetchedPage, error)
}

// HTTPFetcher adapts fetch.Client, the real one.
type HTTPFetcher struct {
	Client *fetch.Client
}

// Fetch retrieves rawURL with the caps and the SSRF guard.
func (f HTTPFetcher) Fetch(ctx context.Context, rawURL string) (*FetchedPage, error) {
	page, err := f.Client.Get(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return &FetchedPage{Body: page.Body, URL: page.URL, ContentType: page.ContentType, close: page.Close}, nil
}

// ClassifyInput is the model's view of the page for the kind decision.
type ClassifyInput struct {
	URL      string
	Title    string
	SiteName string
	Excerpt  string
	Text     string
	IsNote   bool
}

// Classification is the decide answer: one of the twelve kinds, plus the
// optional flags design.md §6 mentions.
type Classification struct {
	Kind      model.Kind
	Paywalled bool
	Video     bool
}

// Decider asks Nova's `decide` surface. It must never answer "unknown".
type Decider interface {
	Decide(ctx context.Context, input ClassifyInput) (Classification, error)
}

// ContentInput is the chat call's view: what the page is, and the tag
// vocabulary the model should reuse.
type ContentInput struct {
	URL        string
	Title      string
	SiteName   string
	Excerpt    string
	Text       string
	Kind       model.Kind
	IsNote     bool
	Vocabulary []string
}

// Content is the chat answer: a cleaned title, a two-sentence summary, 3–6
// tags.
type Content struct {
	Title   string
	Summary string
	Tags    []string
}

// Writer is the tag/summary/title call.
type Writer interface {
	Write(ctx context.Context, input ContentInput) (Content, error)
}

// VideoSource is a video's public metadata: the title, channel and still that
// the page would have carried if it had been readable. Nil disables the
// fallback, which is what the phase-2 shell and most tests want.
type VideoSource interface {
	Metadata(ctx context.Context, url string) (youtube.Metadata, error)
}

// Embedder is Nova's embed surface.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// Store is the part of the database the pipeline writes. It is deliberately
// small: a pipeline that can reach anything is a pipeline whose failures are
// hard to reason about.
type Store interface {
	ApplyEnrichment(ctx context.Context, id string, e store.Enrichment) (model.Item, error)
	ApplyVideoMetadata(ctx context.Context, id string, m store.VideoMetadata) (model.Item, error)
	RecordEnrichmentFailure(ctx context.Context, id, message string, next *time.Time) (model.Item, error)
	TagVocabulary(ctx context.Context, limit int) ([]string, error)
}

// Enricher runs one item at a time. The queue loop calls Process; nothing here
// is concurrent, because one model call at a time is what keeps a small box
// inside its memory budget (design.md §6).
type Enricher struct {
	Fetcher  Fetcher
	Decider  Decider
	Writer   Writer
	Embedder Embedder
	Store    Store
	Log      *slog.Logger

	PromptVersion  string
	ExtractVersion string
	EmbeddingModel string
	Now            func() time.Time

	// Videos is the public metadata fallback for a video whose page could not be
	// read (spike S8: YouTube answers a datacenter fetch with /sorry/).
	Videos VideoSource
}

// New wires the real dependencies with the current versions.
func New(f Fetcher, d Decider, w Writer, e Embedder, s Store, log *slog.Logger) *Enricher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// PromptVersion and EmbeddingModel are deliberately unset. The wiring in
	// cmd/harbor must supply the adapter's constants, and an unset pipeline
	// fails every item loudly instead of writing a row with a guessed model.
	return &Enricher{
		Fetcher: f, Decider: d, Writer: w, Embedder: e, Store: s, Log: log,
		ExtractVersion: DefaultExtractVersion,
		Now:            time.Now,
	}
}

// RetryDelay is how long an item waits after the given failure. Two delays
// before the third (terminal) failure add up to about an hour.
func RetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 15 * time.Minute
	}
	return 45 * time.Minute
}

// Process enriches one item and records either success or failure on the item
// row. It returns the cause for the loop's log line; the loop continues with
// the next item either way.
func (e *Enricher) Process(ctx context.Context, item model.Item) error {
	if e.PromptVersion == "" || e.EmbeddingModel == "" {
		cause := permanent(errors.New("enrich: prompt version and embedding model must be configured"))
		return e.fail(ctx, item, cause)
	}

	// A ready item with the current versions has nothing to do. Re-enrichment
	// is an explicit state change (a new prompt version, a manual retry), never
	// a model call made by accident (plans/03 test 9).
	if item.State == model.StateReady && item.EnrichedAt != nil &&
		item.PromptVersion == e.PromptVersion && item.ExtractVersion == e.ExtractVersion {
		return nil
	}

	if err := e.enrich(ctx, item); err != nil {
		return e.fail(ctx, item, err)
	}
	return nil
}

func (e *Enricher) enrich(ctx context.Context, item model.Item) error {
	var extracted *extract.Result
	if hasURL(item) {
		page, err := e.Fetcher.Fetch(ctx, *item.URL)
		if err != nil {
			cause := classifyFetchError(err)
			if done, handled := e.tryVideoMetadata(ctx, item, cause); done {
				return handled
			}
			return cause
		}
		defer page.Close()

		if !IsHTML(page.ContentType) {
			cause := permanent(fmt.Errorf("content type %q is not HTML", page.ContentType))
			if done, handled := e.tryVideoMetadata(ctx, item, cause); done {
				return handled
			}
			return cause
		}
		base := page.URL
		if base == nil {
			base = &url.URL{}
		}
		extracted, err = extract.HTML(page.Body, base)
		if err != nil {
			cause := permanent(err)
			if done, handled := e.tryVideoMetadata(ctx, item, cause); done {
				return handled
			}
			return cause
		}
		// A wall can answer 200 with a document that has no text at all. For a
		// video that is still recoverable from its metadata; for anything else,
		// the model may still file it from a title and OG tags (The Strand), so
		// the old path stays.
		if strings.TrimSpace(extracted.Text) == "" {
			cause := permanent(errors.New("the page carried no readable text"))
			if done, handled := e.tryVideoMetadata(ctx, item, cause); done {
				return handled
			}
			// A video that has neither a page nor an answer from the metadata
			// endpoint has nothing to read. Handing the model an empty string
			// would produce a summary of nothing and lose the real reason.
			if _, isVideo := youtube.VideoID(*item.URL); isVideo {
				return cause
			}
		}
	}

	kind := item.Kind
	if hasURL(item) && item.KindSource != model.KindSourceManual {
		classification, err := e.Decider.Decide(ctx, e.classifyInput(item, extracted))
		if err != nil {
			return err
		}
		kind = classification.Kind
		if !kind.Valid() {
			e.Log.Warn("model returned a kind outside the twelve; using article", "item", item.ID, "kind", kind)
			kind = model.KindArticle
		}
	}
	if !hasURL(item) {
		// A typed note skips classification entirely (plans/03 test 18).
		kind = model.KindNote
	} else if !kind.Valid() {
		kind = model.KindArticle
	}

	vocabulary, err := e.Store.TagVocabulary(ctx, 50)
	if err != nil {
		// A vocabulary read is a prompt hint, not a save path; enrichment is
		// still worth doing without it.
		e.Log.Warn("tag vocabulary unavailable", "item", item.ID, "error", err)
		vocabulary = nil
	}

	content, err := e.Writer.Write(ctx, e.contentInput(item, extracted, kind, vocabulary))
	if err != nil {
		return err
	}
	cleaned, err := validateContent(content)
	if err != nil {
		return err
	}

	text := extractedText(item, extracted)
	embedInput := embeddingText(firstNonEmpty(cleaned.Title, item.Title), cleaned.Summary, text)
	vec, err := e.Embedder.Embed(ctx, embedInput)
	if err != nil {
		return err
	}
	if len(vec) != EmbeddingDims {
		// Writing a wrong-sized vector would poison phase 10's comparisons.
		return permanent(fmt.Errorf("embedding has %d dimensions, want %d", len(vec), EmbeddingDims))
	}

	enrichment := store.Enrichment{
		Summary:        cleaned.Summary,
		Title:          cleaned.Title,
		Kind:           kind,
		KindSource:     model.KindSourceAI,
		PromptVersion:  e.PromptVersion,
		ExtractVersion: e.ExtractVersion,
		Tags:           cleaned.Tags,
		Embedding:      vec,
		EmbeddingModel: e.EmbeddingModel,
		EmbeddingHash:  hashOf(embedInput),
	}
	if extracted != nil {
		enrichment.ContentText = extracted.Text
		enrichment.ContentMD = extracted.Markdown
		enrichment.DetailsJSON = marshalOrEmpty(extracted.Details)
		enrichment.OGJSON = marshalOrEmpty(extracted.OG)
	}
	if !hasURL(item) {
		enrichment.ContentText = item.Note
	}
	_, err = e.Store.ApplyEnrichment(ctx, item.ID, enrichment)
	return err
}

// tryVideoMetadata files a video the page would not give up: its public
// metadata still carries a title, a channel and a still, and an item with those
// is usable before phase 7 fetches the transcript.
//
// It reports whether it handled the item. False means nothing was written and
// the caller's own failure stands, which is what happens for a non-video URL, a
// pipeline with no video source, or an endpoint that cannot answer either.
func (e *Enricher) tryVideoMetadata(ctx context.Context, item model.Item, cause error) (bool, error) {
	if e.Videos == nil || !hasURL(item) {
		return false, nil
	}
	if _, ok := youtube.VideoID(*item.URL); !ok {
		return false, nil
	}

	meta, err := e.Videos.Metadata(ctx, *item.URL)
	if err != nil {
		// The endpoint could not answer either. The page's failure is the one
		// that explains the item, so it is the one that is kept; this line is
		// for whoever is reading the log.
		e.Log.Warn("video metadata unavailable", "item", item.ID, "error", err)
		return false, nil
	}

	_, err = e.Store.ApplyVideoMetadata(ctx, item.ID, store.VideoMetadata{
		Title:         meta.Title,
		Channel:       meta.Author,
		Thumbnail:     meta.Thumbnail,
		PageURL:       youtube.WatchURL(meta.VideoID),
		Reason:        videoReason(cause),
		PromptVersion: VideoMetadataVersion,
	})
	if err != nil {
		return true, err
	}
	e.Log.Info("filed a video from its public metadata", "item", item.ID, "page_error", cause)
	return true, nil
}

// videoReason is what the row keeps about the page it could not read. Bounded,
// because a fetch error can be long and this is shown to the owner one day.
func videoReason(cause error) string {
	reason := "the page could not be read; filed from the video's public metadata"
	if cause != nil {
		detail := strings.TrimSpace(cause.Error())
		if len(detail) > 120 {
			detail = detail[:117] + "…"
		}
		if detail != "" {
			reason = "the page could not be read (" + detail + "); filed from the video's public metadata"
		}
	}
	return reason
}

// fail advances the retry columns. A permanent error, or the third failure,
// parks the item as failed with the reason kept.
func (e *Enricher) fail(ctx context.Context, item model.Item, cause error) error {
	attempt := item.Attempts + 1
	var next *time.Time
	if !isPermanent(cause) && attempt < MaxAttempts {
		at := e.Now().Add(RetryDelay(attempt))
		next = &at
	}
	if _, err := e.Store.RecordEnrichmentFailure(ctx, item.ID, cause.Error(), next); err != nil {
		e.Log.Error("recording an enrichment failure", "item", item.ID, "error", err)
	}
	e.Log.Warn("enrichment failed",
		"item", item.ID,
		"attempt", attempt,
		"terminal", next == nil,
		"error", cause.Error(),
	)
	return cause
}

func (e *Enricher) classifyInput(item model.Item, extracted *extract.Result) ClassifyInput {
	page := pageText(item, extracted)
	input := ClassifyFor(page)
	input.IsNote = !hasURL(item)
	return input
}

func (e *Enricher) contentInput(item model.Item, extracted *extract.Result, kind model.Kind, vocabulary []string) ContentInput {
	input := ContentFor(pageText(item, extracted), kind)
	input.IsNote = !hasURL(item)
	input.Vocabulary = vocabulary
	return input
}

// pageText is the extracted page, or the note's text when there is no page.
func pageText(item model.Item, extracted *extract.Result) PageText {
	page := PageText{Title: item.Title}
	if hasURL(item) {
		page.URL = *item.URL
	}
	if extracted != nil {
		page.Title = firstNonEmpty(extracted.Title, item.Title)
		page.SiteName = extracted.SiteName
		page.Excerpt = extracted.Excerpt
		page.Text = extracted.Text
		return page
	}
	page.Text = item.Note
	return page
}

func extractedText(item model.Item, extracted *extract.Result) string {
	if extracted != nil {
		return extracted.Text
	}
	return item.Note
}

// validateContent is the shape half of "AI output is never asserted for
// content": a missing summary, an over-long one, or more than maxTags is a
// retry, not a partially written row (plans/03 test 7).
func validateContent(content Content) (Content, error) {
	content.Title = strings.TrimSpace(content.Title)
	content.Summary = strings.TrimSpace(content.Summary)
	if content.Summary == "" {
		return Content{}, fmt.Errorf("the model returned no summary")
	}
	if len(content.Summary) > maxSummaryBytes {
		return Content{}, fmt.Errorf("the model returned a %d-byte summary, cap %d", len(content.Summary), maxSummaryBytes)
	}
	content.Title = truncate(content.Title, maxTitleBytes)

	seen := map[string]bool{}
	tags := make([]string, 0, len(content.Tags))
	for _, raw := range content.Tags {
		name := store.NormaliseTag(raw)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		tags = append(tags, name)
	}
	if len(tags) > maxTags {
		return Content{}, fmt.Errorf("the model returned %d tags, cap %d", len(tags), maxTags)
	}
	content.Tags = tags
	return content, nil
}

// embeddingText is the one input to the embedding model. Title and summary
// carry the meaning; the first embedTextLimit characters of the body carry the
// specifics.
func embeddingText(title, summary, text string) string {
	parts := []string{strings.TrimSpace(title), strings.TrimSpace(summary), truncate(text, embedTextLimit)}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// --- errors ----------------------------------------------------------------

// permanentError is a failure another attempt cannot fix: a 404, a PDF, a
// wrong-sized embedding. Errors default to retryable.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

func permanent(err error) error { return permanentError{err: err} }

func isPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// classifyFetchError turns fetch's typed failures into the retry policy. A
// blocked address, a redirect loop, an oversized body and a 404 are terminal;
// a timeout or a 503 is not (plans/03 test 15).
func classifyFetchError(err error) error {
	var httpErr *fetch.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.Permanent() {
			return permanent(err)
		}
		return err
	}
	for _, terminal := range []error{fetch.ErrBlocked, fetch.ErrTooLarge, fetch.ErrTooManyRedirects, fetch.ErrUnsupportedURL} {
		if errors.Is(err, terminal) {
			return permanent(err)
		}
	}
	return err
}

// IsHTML reports whether a fetched content type is one the pipeline can
// extract. It is exported because the scored set captures pages with the same
// rule: a capture the pipeline would have refused before extraction is not a
// save, and must never become a scored case.
func IsHTML(contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch mediaType {
	case "text/html", "application/xhtml+xml", "application/xml", "text/xml", "text/plain":
		return true
	}
	return false
}

func hasURL(item model.Item) bool {
	return item.URL != nil && strings.TrimSpace(*item.URL) != ""
}

func marshalOrEmpty(value any) string {
	raw, err := json.Marshal(value)
	if err != nil || string(raw) == "null" || string(raw) == "{}" || string(raw) == `[]` {
		return "{}"
	}
	return string(raw)
}

func hashOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
