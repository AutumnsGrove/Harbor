package nova

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

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
