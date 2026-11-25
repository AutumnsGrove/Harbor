// Package nova adapts Nova's decide, chat and embed surfaces to the enrich
// pipeline's interfaces. It is the only package that knows an OpenRouter model
// id; the pipeline knows only the three interfaces.
package nova

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/AutumnsGrove/nova/decide"
	decideor "github.com/AutumnsGrove/nova/decide/openrouter"
	novaembed "github.com/AutumnsGrove/nova/embed"
	embedor "github.com/AutumnsGrove/nova/embed/openrouter"
	"github.com/AutumnsGrove/nova/message"
	"github.com/AutumnsGrove/nova/provider"
	provideror "github.com/AutumnsGrove/nova/provider/openrouter"

	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

const (
	// Models are the ones design.md §6 settled on. A model change is a code
	// change and a fresh prompt version, never a quiet config edit.
	classifyRole  = "classify"
	classifyModel = "typesafe/jev-1.14"
	chatModel     = "deepseek/deepseek-v4.1-flash-1017"
	embedRole     = "index"

	// EmbedModel is exported because the pipeline records it in
	// item_embeddings.model. There is one constant, and the role below and the
	// stored model name both come from it.
	EmbedModel = "perplexity/pplx-embed-v1-0.6b"

	// PromptVersion names the file in prompts/ and is written to
	// items.prompt_version.
	PromptVersion = "tag.v1"

	callTimeout = 45 * time.Second

	// usageWriteTimeout bounds the detached row write. The insert is one small
	// row on a local database; this is only here so a wedged disk cannot hold
	// the enrichment loop.
	usageWriteTimeout = 5 * time.Second
)

//go:embed prompts/*.md
var promptFiles embed.FS

// Config configures the adapter. The key is the only secret; everything else
// has the design's default.
type Config struct {
	APIKey     string
	Log        *slog.Logger
	HTTPClient *http.Client
	Timeout    time.Duration

	// Usage records what each call cost. Nil is a valid state: a score run or a
	// test wants the log line and no database write.
	Usage enrich.UsageSink
}

// Client implements enrich.Decider, enrich.Writer and enrich.Embedder.
type Client struct {
	decider  decide.Decider
	chat     provider.Provider
	embedder novaembed.Embedder
	log      *slog.Logger
	timeout  time.Duration
	usage    enrich.UsageSink
	now      func() time.Time
}

// New builds the three surfaces. It never fails; a missing key is reported by
// the first call, and the pipeline turns that into a visible retry.
func New(cfg Config) *Client {
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = callTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * timeout}
	}

	client := &Client{
		decider: decide.NewRoles(decide.RolesConfig{
			Decider: decideor.New(decideor.Config{APIKey: cfg.APIKey, HTTPClient: httpClient}),
			Roles: map[string]decide.Role{
				classifyRole: {Model: classifyModel, Timeout: timeout},
			},
		}),
		chat: provideror.New(provideror.Config{
			APIKey:     cfg.APIKey,
			HTTPClient: httpClient,
			Title:      "Harbor",
			Referer:    "https://github.com/AutumnsGrove/Harbor",
		}),
		embedder: novaembed.NewRoles(novaembed.RolesConfig{
			Embedder: embedor.New(embedor.Config{APIKey: cfg.APIKey, HTTPClient: httpClient}),
			Roles: map[string]novaembed.Role{
				embedRole: {Model: EmbedModel, Dims: enrich.EmbeddingDims, Timeout: timeout},
			},
		}),
		log:     log,
		timeout: timeout,
		usage:   cfg.Usage,
		now:     time.Now,
	}
	return client
}

// Decide classifies one page. The model never sees "unknown": article is the
// catch-all. The wire client validates the choice against the criteria, so an
// invented kind fails the call; the pipeline's coercion is a second guard for a
// different Decider.
func (c *Client) Decide(ctx context.Context, input enrich.ClassifyInput) (enrich.Classification, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	instructions, err := prompt("classify.v1.md")
	if err != nil {
		return enrich.Classification{}, err
	}
	resp, err := c.decider.Decide(ctx, decide.Request{
		Model: classifyRole,
		State: classifyState(input),
		Questions: map[string]decide.Question{
			"kind": {
				Kind:         decide.KindChoice,
				Instructions: instructions,
				Criteria:     kindCriteria,
			},
			"paywalled": {
				Kind:         decide.KindNoul,
				Instructions: "Is this page a paywalled teaser or a login wall rather than the article?",
			},
			"video": {
				Kind:         decide.KindNoul,
				Instructions: "Is the main content a video rather than a written page?",
			},
		},
	})
	if err != nil {
		// Nova answers with the usage alongside the error when the call itself
		// completed badly (a mid-stream failure, a rejected answer). That call was
		// billed, so it is recorded before the error is returned (issue #52).
		if resp != nil {
			c.recordIfBilled(ctx, "classify", classifyModel, resp.Model, resp.Usage)
		}
		return enrich.Classification{}, fmt.Errorf("nova classify: %w", err)
	}
	// Recorded before the answer is validated: the provider answered and billed,
	// and a rejected answer is still a paid call (issue #52).
	c.logUsage(ctx, "classify", classifyModel, resp.Model, resp.Usage)

	answer, ok := resp.Answers["kind"]
	if !ok || strings.TrimSpace(answer.Choice) == "" {
		return enrich.Classification{}, fmt.Errorf("nova classify: no kind answer")
	}
	return enrich.Classification{
		Kind:      model.Kind(strings.ToLower(strings.TrimSpace(answer.Choice))),
		Paywalled: resp.Answers["paywalled"].Noul >= 0.5,
		Video:     resp.Answers["video"].Noul >= 0.5,
	}, nil
}

// Write asks for the cleaned title, summary and tags as one strict JSON object.
func (c *Client) Write(ctx context.Context, input enrich.ContentInput) (enrich.Content, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	system, err := prompt(PromptVersion + ".md")
	if err != nil {
		return enrich.Content{}, err
	}
	resp, err := c.chat.Stream(ctx, provider.Request{
		Model: chatModel,
		Messages: []message.Message{
			message.System(system),
			message.User(writeState(input)),
		},
		Temperature: floatPtr(0.2),
		MaxTokens:   800,
		ProviderOptions: map[string]any{
			"response_format": map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   "harbor_enrichment",
					"strict": true,
					"schema": enrichmentSchema,
				},
			},
		},
	}, nil)
	if err != nil {
		if resp != nil {
			c.recordIfBilled(ctx, "write", chatModel, resp.Model, resp.Usage)
		}
		return enrich.Content{}, fmt.Errorf("nova write: %w", err)
	}
	c.logUsage(ctx, "write", chatModel, resp.Model, resp.Usage)

	content, err := parseContent(resp.Message.Content)
	if err != nil {
		return enrich.Content{}, err
	}
	return content, nil
}

// Embed turns one text into a vector. Nova's Roles enforces 1024 dimensions;
// the pipeline checks again before storing.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.embedder.Embed(ctx, novaembed.Request{Model: embedRole, Input: []string{text}})
	if err != nil {
		// novaembed.Roles answers a wrong dimension with the provider's usage and
		// an error, which is the one path that both bills and fails in production.
		if resp != nil {
			c.recordIfBilled(ctx, "embed", EmbedModel, resp.Model, resp.Usage)
		}
		return nil, fmt.Errorf("nova embed: %w", err)
	}
	// Same rule as Decide and Write: the call is recorded before the shape of the
	// answer is judged, because the bill does not depend on it (issue #52).
	c.logUsage(ctx, "embed", EmbedModel, resp.Model, resp.Usage)

	if len(resp.Vectors) != 1 {
		return nil, fmt.Errorf("nova embed: got %d vectors for one input", len(resp.Vectors))
	}
	return resp.Vectors[0], nil
}

// recordIfBilled records usage that came back attached to an error. A transport
// failure answers a zero Usage and no model — nothing was billed, and a row for
// it would be a phantom call — while a rejected answer, a mid-stream failure or
// a dimension error carries the provider's real numbers and must be recorded.
func (c *Client) recordIfBilled(ctx context.Context, surface, requested, served string, usage provider.Usage) {
	if usage.CostUSD == 0 && usage.PromptTokens == 0 && usage.CompletionTokens == 0 && served == "" {
		return
	}
	c.logUsage(ctx, surface, requested, served, usage)
}

// logUsage is the one place a call is recorded, so the log line and the row can
// never disagree. The row is written even when the answer is later rejected: the
// provider billed for it. A failed write is a warning — the answer is already in
// hand (issue #52).
func (c *Client) logUsage(ctx context.Context, surface, requested, served string, usage provider.Usage) {
	c.log.Info("nova call",
		"surface", surface,
		"model", requested,
		"served", served,
		"prompt_tokens", usage.PromptTokens,
		"completion_tokens", usage.CompletionTokens,
		"cost_usd", usage.CostUSD,
	)

	if c.usage == nil {
		return
	}
	// The call this records often failed by deadline or cancellation, so ctx is
	// already dead — and RecordUsage is an ExecContext, which would reject the
	// insert and lose exactly the row this exists to keep. Detach the write from
	// the call (the same rule the importer's bookkeeping uses) and still bound
	// it, so a wedged database cannot hold the drain.
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), usageWriteTimeout)
	defer cancel()

	err := c.usage.RecordUsage(write, store.Usage{
		At:               c.now(),
		Surface:          surface,
		RequestedModel:   requested,
		ServedModel:      served,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		CostUSD:          usage.CostUSD,
		ItemID:           enrich.UsageItem(ctx),
	})
	if err != nil {
		c.log.Warn("recording a model call failed", "surface", surface, "error", err)
	}
}

// kindCriteria is the twelve kinds and what they mean. It is the option set the
// decide API validates against, so it cannot drift from model.Kinds() without
// this file changing too.
var kindCriteria = map[string]string{
	string(model.KindArticle): "a written page, a repository, or anything that does not clearly fit below",
	string(model.KindProduct): "a page selling a physical or digital product",
	string(model.KindBook):    "a book, an ebook, or a book's page",
	string(model.KindRecipe):  "a cooking recipe",
	string(model.KindVideo):   "a video, a talk, or a video platform page",
	string(model.KindTweet):   "a short social post",
	string(model.KindNote):    "a note the owner typed, with no page",
	string(model.KindImage):   "an image or a gallery",
	string(model.KindPaper):   "an academic paper or a preprint",
	string(model.KindPodcast): "a podcast episode or a show page",
	string(model.KindPlace):   "a restaurant, shop, cafe or other place",
	string(model.KindMovie):   "a film or a TV title",
}

var enrichmentSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"title", "summary", "tags"},
	"properties": map[string]any{
		"title":   map[string]any{"type": "string"},
		"summary": map[string]any{"type": "string"},
		"tags": map[string]any{
			"type":     "array",
			"minItems": 3,
			"maxItems": 6,
			"items":    map[string]any{"type": "string"},
		},
	},
}

// parseContent accepts a bare JSON object or one wrapped in a code fence. A
// page of prose around it is a malformed answer, and the pipeline retries.
func parseContent(raw string) (enrich.Content, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	if start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); start >= 0 && end > start {
		raw = raw[start : end+1]
	}

	var parsed struct {
		Title   string   `json:"title"`
		Summary string   `json:"summary"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return enrich.Content{}, fmt.Errorf("nova write: answer is not JSON: %w", err)
	}
	return enrich.Content{Title: parsed.Title, Summary: parsed.Summary, Tags: parsed.Tags}, nil
}

// classifyState and writeState label the untrusted page text, so an injection
// inside the page reads as data rather than as the caller's instruction.
func classifyState(input enrich.ClassifyInput) string {
	var b strings.Builder
	b.WriteString("URL: " + input.URL + "\n")
	b.WriteString("Title: " + input.Title + "\n")
	b.WriteString("Site: " + input.SiteName + "\n")
	b.WriteString("Excerpt: " + input.Excerpt + "\n")
	if input.IsNote {
		b.WriteString("This is a typed note with no page.\n")
	}
	b.WriteString("Page text (untrusted):\n" + input.Text)
	return b.String()
}

func writeState(input enrich.ContentInput) string {
	var b strings.Builder
	b.WriteString("Kind: " + string(input.Kind) + "\n")
	b.WriteString("URL: " + input.URL + "\n")
	b.WriteString("Title: " + input.Title + "\n")
	b.WriteString("Site: " + input.SiteName + "\n")
	b.WriteString("Excerpt: " + input.Excerpt + "\n")
	if len(input.Vocabulary) > 0 {
		b.WriteString("Vocabulary: " + strings.Join(input.Vocabulary, ", ") + "\n")
	}
	if input.IsNote {
		b.WriteString("The owner's note (untrusted):\n" + input.Text)
		return b.String()
	}
	b.WriteString("Page text (untrusted):\n" + input.Text)
	return b.String()
}

func prompt(name string) (string, error) {
	raw, err := promptFiles.ReadFile("prompts/" + name)
	if err != nil {
		return "", fmt.Errorf("nova: reading prompt %s: %w", name, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func floatPtr(v float64) *float64 { return &v }
