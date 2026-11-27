// Package enrich is the pipeline that turns a queued item into a filed one:
// fetch, extract, classify, tag/summarise, embed, write (design.md §6).
//
// It depends on interfaces, not on Nova or HTTP, so the whole pipeline is
// testable with recorded fixtures and a real database. The Nova adapters live
// beside it and implement these three interfaces.
package enrich

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/clip"
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

	// WallVersion and DeadVersion are what a row records when the page would not
	// be read: a site that refuses this client, and a page that is gone. They are
	// not prompts — no model ran — which is the point: the API badges a row by
	// them and a manual retry clears them (design §6, issues #76, #79). The
	// values live in model, because the badge is decided there too.
	WallVersion     = model.WallVersion
	DeadVersion     = model.DeadVersion
	NoTextVersion   = model.NoTextVersion
	TooLargeVersion = model.TooLargeVersion

	// EmbeddingDims is pplx-embed-v1-0.6b's dimension (spike S5). A different
	// model means changing this deliberately, not silently accepting a vector
	// of the wrong size.
	EmbeddingDims = 1024

	// MaxAttempts is three tries in total for a broken answer, matching the
	// "could not be enriched after three tries" copy in the attention strip.
	MaxAttempts = 3

	// RateLimitedAttempts is the longer budget for a site that asked us to come
	// back later. A 429 is a promise, not a fault, so parking it inside the hour
	// would throw away a save a slower retry would file (design §6).
	RateLimitedAttempts = 5

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
	// Size is how many bytes Body holds, used by the PDF reader, which needs an
	// offset-addressed view rather than a stream (issue #74). Zero is allowed:
	// the reader measures the body itself in that case.
	Size  int64
	close func() error
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
	return &FetchedPage{Body: page.Body, URL: page.URL, ContentType: page.ContentType, Size: page.Size, close: page.Close}, nil
}

// looksLikePDF reports whether the bytes start with %PDF-, and leaves the body
// where it found it. The content type is not the test: a CDN that labels a paper
// application/octet-stream, or sends nothing at all, still hands over a
// document, and refusing it would fail the save for the wrong reason (issue
// #74).
func (p *FetchedPage) looksLikePDF() (bool, error) {
	head := make([]byte, 5)
	n, err := io.ReadFull(p.Body, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("enrich: reading the head: %w", err)
	}
	if _, err := p.Body.Seek(0, io.SeekStart); err != nil {
		return false, fmt.Errorf("enrich: rewinding: %w", err)
	}
	return extract.LooksLikePDF(head[:n]), nil
}

// pdf reads the document. The reader needs offsets, not a stream, and the body
// is a temp file or a fake in memory; anything else is read into memory, which
// the fetch cap already bounds.
func (p *FetchedPage) pdf() (*extract.Result, error) {
	name := ""
	if p.URL != nil {
		name = path.Base(p.URL.Path)
	}
	size := p.Size
	if size <= 0 {
		end, err := p.Body.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, fmt.Errorf("enrich: sizing the document: %w", err)
		}
		size = end
	}
	if reader, ok := p.Body.(io.ReaderAt); ok {
		return extract.PDF(reader, size, name)
	}
	// A body that cannot be addressed is read once and addressed in memory; the
	// fetch cap bounds it, and the temp file the real fetcher hands over is
	// already an *os.File.
	if _, err := p.Body.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("enrich: rewinding the document: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(p.Body, size))
	if err != nil {
		return nil, fmt.Errorf("enrich: reading the document: %w", err)
	}
	return extract.PDF(bytes.NewReader(body), int64(len(body)), name)
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
	ApplyUnreadable(ctx context.Context, id string, u store.Unreadable) (model.Item, error)
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
// before the third (terminal) failure add up to about an hour. A rate-limited
// answer uses RateLimitedDelay instead, so it can outlive that budget.
func RetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 15 * time.Minute
	}
	return 45 * time.Minute
}

// RateLimitedDelay is the wait after a 429, by attempt. It runs longer than a
// broken answer's because the site told us the page is still there.
func RateLimitedDelay(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return 15 * time.Minute
	case attempt == 2:
		return 45 * time.Minute
	case attempt == 3:
		return 3 * time.Hour
	default:
		return 12 * time.Hour
	}
}

// retriesRateLimited reports a site that asked us to slow down.
func retriesRateLimited(err error) bool {
	var httpErr *fetch.HTTPError
	return errors.As(err, &httpErr) && httpErr.RateLimited()
}

// Process enriches one item and records either success or failure on the item
// row. It returns the cause, which the loop counts; the single log line for a
// failed item is written here, by the layer that knows whether the row was
// parked or scheduled (issue #80).
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
	// The adapter records what each call cost against this item; the pipeline is
	// the only place that knows which item a call belongs to (issue #52).
	ctx = WithUsageItem(ctx, item.ID)

	var extracted *extract.Result
	if hasURL(item) {
		page, err := e.Fetcher.Fetch(ctx, *item.URL)
		if err != nil {
			// A body over its cap is deterministic: no retry makes a 52 MB file
			// smaller, so the row is filed with the number that refused it rather
			// than parked in Needs attention forever (issue #74). The error text
			// carries both the size and the cap.
			if errors.Is(err, fetch.ErrTooLarge) {
				if done, handled := e.tryVideoMetadata(ctx, item, nil, err); done || handled != nil {
					return handled
				}
				return e.fileUnreadable(ctx, item, nil, refusal{
					marker: TooLargeVersion,
					reason: shorten(err, 200) + "; filed from what the save has, with no summary or tags",
				})
			}
			cause := classifyFetchError(err)
			// A video is still worth a real row even when its page refuses us;
			// the zero-cost metadata endpoint is the better answer, so it is
			// tried before anything is filed thin.
			if done, handled := e.tryVideoMetadata(ctx, item, nil, cause); done || handled != nil {
				return handled
			}
			if refusal, ok := refusalOf(err); ok {
				return e.fileUnreadable(ctx, item, nil, refusal)
			}
			return cause
		}
		defer page.Close()

		base := page.URL
		if base == nil {
			base = &url.URL{}
		}

		// A PDF is recognised by its bytes before the content type is consulted:
		// a document that arrives labelled octet-stream is still a document, and
		// its reader is the only one that needs offsets rather than a stream.
		isPDF, err := page.looksLikePDF()
		if err != nil {
			return permanent(err)
		}

		// Four readable shapes, one refusal. A Markdown or plain-text body is read
		// as text instead of being fed to an HTML parser that would find no
		// article in it (issue #73), a PDF is read for its text layer (issue #74),
		// and anything else — an image, a zip — is still a permanent failure until
		// its own phase gives it a reader.
		switch {
		case isPDF:
			extracted, err = page.pdf()
		case extract.IsHTML(page.ContentType):
			extracted, err = extract.HTML(page.Body, base)
		case extract.IsDocument(page.ContentType):
			extracted, err = extract.Document(page.Body, page.ContentType, base)
		default:
			cause := permanent(fmt.Errorf("content type %q is not readable", page.ContentType))
			if done, handled := e.tryVideoMetadata(ctx, item, extracted, cause); done || handled != nil {
				return handled
			}
			return cause
		}
		if err != nil {
			// A document that will not open is not a failed save, it is a save
			// with nothing to read in it (issue #74). Everything else keeps the
			// old rule: a page that will not parse can still be filed by hand or
			// retried, and a silent pass would hide a real parser bug.
			if isPDF {
				return e.fileUnreadable(ctx, item, extracted, refusal{
					marker: model.NoTextVersion,
					reason: "the document could not be read (" + shorten(err, 120) + "); filed from its title, with no summary or tags",
				})
			}
			cause := permanent(err)
			if done, handled := e.tryVideoMetadata(ctx, item, extracted, cause); done || handled != nil {
				return handled
			}
			return cause
		}
		// A document with no text layer is a scan, and a scan gets its own answer:
		// filed ready and titled, with the reason said plainly, rather than a
		// model call that would invent a summary of a title (issue #74). OCR is
		// issue #99 and this row is what it will pick up.
		if extracted != nil && extracted.PDF != nil && extracted.PDF.TextState == extract.TextStateEmpty {
			return e.fileUnreadable(ctx, item, extracted, refusal{
				marker: model.NoTextVersion,
				reason: fmt.Sprintf("the document has no text layer: a scan of %d page(s); filed from its title, with no summary or tags until OCR lands (issue #99)", extracted.PDF.Pages),
			})
		}
		// A wall can answer 200 with a document that has no text at all. For a
		// video that is still recoverable from its metadata; for anything else,
		// the model may still file it from a title and OG tags (The Strand), so
		// the old path stays.
		if strings.TrimSpace(extracted.Text) == "" {
			cause := permanent(errors.New("the page carried no readable text"))
			if done, handled := e.tryVideoMetadata(ctx, item, extracted, cause); done || handled != nil {
				return handled
			}
			// A video that has neither a page nor a metadata source has nothing
			// to read. Handing the model an empty string would produce a
			// summary of nothing and lose the real reason.
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
		// Store the cover the extractor resolved against the page it actually
		// fetched, not the raw og:image. A relative image on a redirected page
		// would otherwise be resolved later against the saved URL — the
		// shortener's host — and never fetch (issue #63).
		og := extracted.OG
		if extracted.CoverURL != "" {
			og.Image = extracted.CoverURL
		}
		enrichment.OGJSON = marshalOrEmpty(og)
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
// extracted is the page as far as it was read, which may be nil (the fetch
// failed) or carry only Open Graph metadata (the page parsed to no article). It
// matters for one outcome: a video the endpoint refuses to describe still has a
// title from its own page or from the save, and taking that is the difference
// between an item and a row in Needs attention (issue #79).
//
// The two return values separate the outcomes, and the caller needs all of them:
//
//   - handled: the row was written (or the write failed, which is the error).
//   - not a video, or no video source: (false, nil) — the caller's own failure
//     stands.
//   - a video with nothing left to try: (false, err) with err classified. A
//     definitive answer is permanent; a timeout or a 429 is not, and must keep
//     the retry schedule the page's own failure would have given it.
func (e *Enricher) tryVideoMetadata(ctx context.Context, item model.Item, extracted *extract.Result, cause error) (bool, error) {
	if e.Videos == nil || !hasURL(item) {
		return false, nil
	}
	if _, ok := youtube.VideoID(*item.URL); !ok {
		return false, nil
	}

	meta, err := e.Videos.Metadata(ctx, *item.URL)
	if err != nil {
		// No log line here: the error is returned, and whichever outcome it leads
		// to — filed anyway, or parked with a reason — is logged once by the
		// layer that knows which (issue #80).
		if errors.Is(err, youtube.ErrRefused) {
			return e.fileRefusedVideo(ctx, item, extracted, err)
		}
		if errors.Is(err, youtube.ErrNoMetadata) {
			return false, permanent(err)
		}
		return false, err
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

// fileRefusedVideo keeps a video the endpoint declined to describe. An owner who
// disabled embedding produces a 401 or a 403 for a video that plays perfectly
// well, so the refusal says nothing about whether the item is worth keeping —
// and the item already has a real URL and usually a real title.
//
// What it does not have is a summary and tags: those need the model, and the
// model needs text this item never had. So the row is filed ready, with the
// reason it is thin, and phase 7 can still give it a transcript.
func (e *Enricher) fileRefusedVideo(ctx context.Context, item model.Item, extracted *extract.Result, cause error) (bool, error) {
	title := videoFallbackTitle(item, extracted)
	if title == "" {
		// Nothing to file it under: no page title, no metadata, and the saved
		// title is only the URL. The refusal stands as a failure, honestly.
		return false, permanent(fmt.Errorf("the video's metadata refused this one and it has no title to file it under: %w", cause))
	}

	cover := ""
	if extracted != nil {
		cover = extracted.CoverURL
	}
	_, err := e.Store.ApplyVideoMetadata(ctx, item.ID, store.VideoMetadata{
		Title:         title,
		Thumbnail:     cover,
		PageURL:       youtube.WatchURL(youtubeIDOf(*item.URL)),
		Reason:        refusedVideoReason(cause),
		PromptVersion: VideoMetadataVersion,
	})
	if err != nil {
		return true, err
	}
	e.Log.Info("filed a refused video from the title it already had", "item", item.ID, "page_error", cause)
	return true, nil
}

// videoFallbackTitle is the best title available when the endpoint refuses the
// page: its own og:title, else the title the save already carried — unless that
// title is just the URL, which is not a title.
func videoFallbackTitle(item model.Item, extracted *extract.Result) string {
	if extracted != nil {
		if title := strings.TrimSpace(extracted.Title); title != "" {
			return title
		}
	}
	title := strings.TrimSpace(item.Title)
	if item.URL != nil && title == strings.TrimSpace(*item.URL) {
		return ""
	}
	return title
}

// youtubeIDOf is the id for a URL that has already been proven to carry one; it
// is only called from the branches that checked.
func youtubeIDOf(rawURL string) string {
	id, _ := youtube.VideoID(rawURL)
	return id
}

// refusedVideoReason is what the row keeps about a video nobody described. It is
// the amber strip on the item, so it says what is missing rather than what broke.
func refusedVideoReason(cause error) string {
	detail := shorten(cause, 120)
	if detail == "" {
		return "the video's public metadata refuses this one (embedding is disabled); filed from its title, with no summary or tags"
	}
	return "the video's public metadata refuses this one (" + detail + "); filed from its title, with no summary or tags"
}

// shorten bounds an error for a field shown to the owner: the reason is one line,
// and an HTTP error can be long. The cutting rule is clip's, so the bound is the
// bound and the cut lands on a rune (issues #89, #90).
func shorten(err error, max int) string {
	if err == nil {
		return ""
	}
	return clip.Text(err.Error(), max)
}

// videoReason is what the row keeps about the page it could not read. Bounded,
// because a fetch error can be long and this is shown to the owner one day.
func videoReason(cause error) string {
	reason := "the page could not be read; filed from the video's public metadata"
	if detail := shorten(cause, 120); detail != "" {
		reason = "the page could not be read (" + detail + "); filed from the video's public metadata"
	}
	return reason
}

// fail advances the retry columns. A permanent error, or the third failure,
// parks the item as failed with the reason kept.
//
// A cancelled run is not an outcome: if the context died while this item was
// being worked on, nothing is recorded, no retry is scheduled and no failure is
// claimed. The row keeps the state it had, and the next pass picks it up — which
// is the whole point of a resumable drain. Recording it would also fail, because
// the write needs the same dead context (CLAUDE.md's class, issue #80).
func (e *Enricher) fail(ctx context.Context, item model.Item, cause error) error {
	if err := ctx.Err(); err != nil {
		e.Log.Info("enrichment stopped mid-item; the row is unchanged", "item", item.ID, "reason", err.Error())
		return err
	}

	attempt := item.Attempts + 1
	var next *time.Time
	if !isPermanent(cause) {
		switch {
		case retriesRateLimited(cause):
			// A 429 outlives the broken budget: the site said the page is there.
			if attempt < RateLimitedAttempts {
				at := e.Now().Add(RateLimitedDelay(attempt))
				next = &at
			}
		default:
			if attempt < MaxAttempts {
				at := e.Now().Add(RetryDelay(attempt))
				next = &at
			}
		}
	}
	if _, err := e.Store.RecordEnrichmentFailure(ctx, item.ID, cause.Error(), next); err != nil {
		e.Log.Error("recording an enrichment failure", "item", item.ID, "error", err)
	}
	// One line per item outcome, and the level is the outcome: a row that is now
	// parked with a reason is a fact, and a row that will be tried again is a
	// warning. This used to be two lines at WARN plus a third at ERROR from the
	// loop, which made a night of 11 parked rows read as an error storm (issue
	// #80).
	if next == nil {
		e.Log.Info("enrichment parked the item",
			"item", item.ID,
			"attempts", attempt,
			"error", cause.Error(),
		)
	} else {
		e.Log.Warn("enrichment failed; a retry is scheduled",
			"item", item.ID,
			"attempt", attempt,
			"retry_at", next.Format(time.RFC3339),
			"error", cause.Error(),
		)
	}
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

// --- a page that will not be read -----------------------------------------

// refusal is a page that answered and still cannot be read: the site refusing
// this client, or the page being gone. Neither is a failure — the save is hers
// — so the row is filed with what it already carries and a marker saying why it
// is thin (design §6, issue #76).

type refusal struct {
	marker string
	reason string
	dead   bool
}

// refusalOf turns a fetch error into a refusal, or reports that this is not one
// of those answers. A wall is 401, 403 and 451; a death is 404, 410 and a host
// that no longer resolves. A sinkholed or private answer is not a refusal: that
// is the guard doing its job, and it is reported as what it is.
func refusalOf(err error) (refusal, bool) {
	var httpErr *fetch.HTTPError
	if errors.As(err, &httpErr) {
		switch {
		case httpErr.Walled():
			return refusal{
				marker: WallVersion,
				reason: "the site refuses this client (" + shorten(err, 120) + "); filed from what the save has, with no summary or tags",
			}, true
		case httpErr.Dead():
			return refusal{
				marker: DeadVersion,
				reason: "the page is gone (" + shorten(err, 120) + "); filed from what the save has, with no summary or tags",
				dead:   true,
			}, true
		}
	}
	if errors.Is(err, fetch.ErrDeadLink) {
		return refusal{
			marker: DeadVersion,
			reason: "the host no longer resolves (" + shorten(err, 120) + "); filed from what the save has, with no summary or tags",
			dead:   true,
		}, true
	}
	return refusal{}, false
}

// fileUnreadable writes the row the page would not give up. It is the same
// shape as filing a refused video: ready, titled, and honest about what it does
// not have — and no model call, because there is no text to read.
func (e *Enricher) fileUnreadable(ctx context.Context, item model.Item, extracted *extract.Result, r refusal) error {
	title := ""
	if extracted != nil {
		title = strings.TrimSpace(extracted.Title)
	}
	_, err := e.Store.ApplyUnreadable(ctx, item.ID, store.Unreadable{
		Title:  title,
		Reason: r.reason,
		Marker: r.marker,
		Dead:   r.dead,
	})
	if err != nil {
		return err
	}
	e.Log.Info("filed a save the page would not give up",
		"item", item.ID,
		"outcome", r.marker,
		"reason", r.reason,
	)
	return nil
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
	// A name that does not resolve is a dead link, handled before this function
	// is asked what to retry; listing it here keeps a caller that forgets from
	// spending a retry budget on a host that is not there.
	for _, terminal := range []error{fetch.ErrBlocked, fetch.ErrTooLarge, fetch.ErrTooManyRedirects, fetch.ErrUnsupportedURL, fetch.ErrDeadLink} {
		if errors.Is(err, terminal) {
			return permanent(err)
		}
	}
	return err
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
