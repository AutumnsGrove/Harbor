package nova

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/AutumnsGrove/nova/decide"
	novaembed "github.com/AutumnsGrove/nova/embed"
	"github.com/AutumnsGrove/nova/provider"

	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Issue #52's reconciliation, at the seam that matters: the row stores exactly
// what the provider reported, and the log line and the row are written in the
// same place, so they cannot disagree.

type recordingSink struct {
	rows []store.Usage
	err  error
}

func (s *recordingSink) RecordUsage(_ context.Context, usage store.Usage) error {
	if s.err != nil {
		return s.err
	}
	s.rows = append(s.rows, usage)
	return nil
}

func TestLogUsageRecordsWhatTheProviderReported(t *testing.T) {
	sink := &recordingSink{}
	client := New(Config{APIKey: "test", Log: slog.New(slog.DiscardHandler), Usage: sink})
	client.now = func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }

	reported := provider.Usage{PromptTokens: 1234, CompletionTokens: 56, CostUSD: 0.001234}
	ctx := enrich.WithUsageItem(context.Background(), "item-7")
	client.logUsage(ctx, "write", "deepseek/deepseek-v4.1-flash-1017", "deepseek/deepseek-v4.1-flash-1017", reported)

	if len(sink.rows) != 1 {
		t.Fatalf("recorded %d rows, want 1", len(sink.rows))
	}
	row := sink.rows[0]
	// Not an estimate and not a re-derivation: the provider's own numbers.
	if row.PromptTokens != reported.PromptTokens || row.CompletionTokens != reported.CompletionTokens || row.CostUSD != reported.CostUSD {
		t.Errorf("row = %+v, want the provider's %+v", row, reported)
	}
	if row.Surface != "write" {
		t.Errorf("surface = %q, want write", row.Surface)
	}
	if row.RequestedModel != chatModel || row.ServedModel != chatModel {
		t.Errorf("model = %q/%q, want %q", row.RequestedModel, row.ServedModel, chatModel)
	}
	if row.ItemID != "item-7" {
		t.Errorf("item id = %q, want item-7 from the context", row.ItemID)
	}
	if !row.At.Equal(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("at = %v, want the client's clock", row.At)
	}
}

// A sink with no item (a score run, a probe) still records; the id is empty.
func TestLogUsageWithoutAnItemStillRecords(t *testing.T) {
	sink := &recordingSink{}
	client := New(Config{APIKey: "test", Log: slog.New(slog.DiscardHandler), Usage: sink})

	client.logUsage(context.Background(), "embed", EmbedModel, EmbedModel,
		provider.Usage{PromptTokens: 10, CostUSD: 0.00001})

	if len(sink.rows) != 1 || sink.rows[0].ItemID != "" {
		t.Fatalf("rows = %+v, want one row with no item", sink.rows)
	}
}

// The drain must never be slowed or stopped by a broken usage write: the answer
// is already in hand.
func TestLogUsageSwallowsASinkFailure(t *testing.T) {
	sink := &recordingSink{err: errors.New("disk on fire")}
	client := New(Config{APIKey: "test", Log: slog.New(slog.DiscardHandler), Usage: sink})

	client.logUsage(context.Background(), "write", chatModel, chatModel, provider.Usage{CostUSD: 1})

	// Reaching here without a panic is the assertion; the log line is the
	// caller's only trace, and the enrichment continues.
}

// No sink is a valid configuration, not a crash.
func TestLogUsageWithNoSinkIsFine(t *testing.T) {
	client := New(Config{APIKey: "test", Log: slog.New(slog.DiscardHandler)})
	client.logUsage(context.Background(), "write", chatModel, chatModel, provider.Usage{CostUSD: 1})
}

// pi-review on #64: the row was written after the local answer checks, so a call
// the provider answered and billed vanished when the answer was rejected. Both
// surfaces are covered here.

type billedDecider struct {
	usage provider.Usage
}

func (d billedDecider) Decide(context.Context, decide.Request) (*decide.Response, error) {
	// An answer with no "kind": the provider billed for this call.
	return &decide.Response{Answers: map[string]decide.Answer{}, Usage: d.usage, Model: "typesafe/jev-1.14"}, nil
}

type billedEmbedder struct {
	usage provider.Usage
}

func (e billedEmbedder) Embed(context.Context, novaembed.Request) (*novaembed.Response, error) {
	// Zero vectors: the answer is unusable, but the call was paid for.
	return &novaembed.Response{Usage: e.usage, Model: EmbedModel}, nil
}

func TestDecideRecordsABilledCallWhoseAnswerIsRejected(t *testing.T) {
	sink := &recordingSink{}
	client := &Client{
		decider: billedDecider{usage: provider.Usage{PromptTokens: 10, CostUSD: 0.5}},
		log:     slog.New(slog.DiscardHandler),
		timeout: time.Second,
		usage:   sink,
		now:     time.Now,
	}

	if _, err := client.Decide(context.Background(), enrich.ClassifyInput{URL: "https://example.test"}); err == nil {
		t.Fatal("Decide answered a response with no kind; want an error")
	}
	if len(sink.rows) != 1 {
		t.Fatalf("recorded %d rows, want 1: the rejected answer was still billed", len(sink.rows))
	}
	if sink.rows[0].CostUSD != 0.5 || sink.rows[0].Surface != "classify" {
		t.Errorf("row = %+v, want the classify call's 0.5", sink.rows[0])
	}
}

func TestEmbedRecordsABilledCallWhoseAnswerIsRejected(t *testing.T) {
	sink := &recordingSink{}
	client := &Client{
		embedder: billedEmbedder{usage: provider.Usage{PromptTokens: 7, CostUSD: 0.25}},
		log:      slog.New(slog.DiscardHandler),
		timeout:  time.Second,
		usage:    sink,
		now:      time.Now,
	}

	if _, err := client.Embed(context.Background(), "text"); err == nil {
		t.Fatal("Embed answered zero vectors; want an error")
	}
	if len(sink.rows) != 1 {
		t.Fatalf("recorded %d rows, want 1: the rejected answer was still billed", len(sink.rows))
	}
	if sink.rows[0].CostUSD != 0.25 || sink.rows[0].Surface != "embed" {
		t.Errorf("row = %+v, want the embed call's 0.25", sink.rows[0])
	}
}

// The production path this class of bug lives on: novaembed.Roles validates the
// answer's dimensions and returns the provider's usage *with* the error. Using
// the real decorator is the point — a bare fake never reaches this code.
type shortVectorEmbedder struct{ calls int }

func (s *shortVectorEmbedder) Embed(context.Context, novaembed.Request) (*novaembed.Response, error) {
	s.calls++
	return &novaembed.Response{
		Vectors: [][]float32{make([]float32, 3)},
		Dims:    3,
		Model:   EmbedModel,
		Usage:   provider.Usage{PromptTokens: 9, CostUSD: 0.42},
	}, nil
}

func TestEmbedRecordsABilledCallTheRolesDecoratorRejects(t *testing.T) {
	roles := novaembed.NewRoles(novaembed.RolesConfig{
		Embedder: &shortVectorEmbedder{},
		Roles: map[string]novaembed.Role{
			embedRole: {Model: EmbedModel, Dims: enrich.EmbeddingDims, Timeout: time.Second},
		},
	})

	sink := &recordingSink{}
	client := &Client{
		embedder: roles,
		log:      slog.New(slog.DiscardHandler),
		timeout:  time.Second,
		usage:    sink,
		now:      time.Now,
	}

	if _, err := client.Embed(context.Background(), "text"); err == nil {
		t.Fatal("Embed accepted a 3-component answer for a 1024-dimension role")
	}
	if len(sink.rows) != 1 {
		t.Fatalf("recorded %d rows, want 1: novaembed.Roles billed for this call", len(sink.rows))
	}
	if sink.rows[0].CostUSD != 0.42 || sink.rows[0].Surface != "embed" {
		t.Errorf("row = %+v, want the embed call's 0.42", sink.rows[0])
	}
}

type failingDecider struct{ resp *decide.Response }

func (d failingDecider) Decide(context.Context, decide.Request) (*decide.Response, error) {
	return d.resp, errors.New("stream broke mid-answer")
}

func TestDecideRecordsUsageThatArrivesWithAnError(t *testing.T) {
	sink := &recordingSink{}
	client := &Client{
		decider: failingDecider{resp: &decide.Response{
			Model: classifyModel,
			Usage: provider.Usage{PromptTokens: 5, CompletionTokens: 1, CostUSD: 0.02},
		}},
		log:     slog.New(slog.DiscardHandler),
		timeout: time.Second,
		usage:   sink,
		now:     time.Now,
	}

	if _, err := client.Decide(context.Background(), enrich.ClassifyInput{URL: "https://example.test"}); err == nil {
		t.Fatal("Decide ignored a stream error")
	}
	if len(sink.rows) != 1 || sink.rows[0].CostUSD != 0.02 {
		t.Fatalf("rows = %+v, want the partial answer's 0.02", sink.rows)
	}
}

// A transport failure answers an empty response and no usage: nothing was
// billed, so a row for it would be a phantom call.
func TestTransportFailuresDoNotRecordPhantomRows(t *testing.T) {
	sink := &recordingSink{}
	client := &Client{
		decider: failingDecider{resp: &decide.Response{}},
		log:     slog.New(slog.DiscardHandler),
		timeout: time.Second,
		usage:   sink,
		now:     time.Now,
	}

	if _, err := client.Decide(context.Background(), enrich.ClassifyInput{URL: "https://example.test"}); err == nil {
		t.Fatal("Decide ignored the error")
	}
	if len(sink.rows) != 0 {
		t.Errorf("recorded %+v, want no row: a refused connection is not a billed call", sink.rows)
	}
}
