package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// EnrichStore is the part of the store the phase-2 enrichment stage needs.
// Phase 3 adds the failure path; the shape is deliberately two methods so the
// first version cannot grow a model call by accident.
type EnrichStore interface {
	QueuedItems(ctx context.Context, limit int) ([]model.Item, error)
	MarkReady(ctx context.Context, id string) error
}

// Enricher is the enrichment stage before there is enrichment: it drains the
// queued items in priority order and marks them ready. That is a real state
// change, not a stub in the UI, and it means `/api/status`'s pending count
// returns to zero (design.md §5, plans/02).
type Enricher struct {
	store   EnrichStore
	log     *slog.Logger
	cadence time.Duration
	batch   int

	mu      sync.Mutex
	lastRun time.Time
	lastErr string
}

// NewEnricher builds the production stage: every five seconds, fifty items at
// a time.
func NewEnricher(s EnrichStore, log *slog.Logger) *Enricher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Enricher{store: s, log: log, cadence: 5 * time.Second, batch: 50}
}

// Status is the loop's health in one sentence: when it last ran and what went
// wrong. It is logged rather than served until phase 12b's Status page.
func (e *Enricher) Status() (time.Time, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastRun, e.lastErr
}

// Run is the goroutine. One pass happens immediately, so a restart does not
// leave saves queued for a cadence.
func (e *Enricher) Run(ctx context.Context) {
	e.log.Info("enrichment loop started", "cadence", e.cadence.String())
	defer e.log.Info("enrichment loop stopped")

	ticker := time.NewTicker(e.cadence)
	defer ticker.Stop()
	for {
		if n, err := e.Pass(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			e.log.Error("enrichment pass failed", "error", err)
		} else if n > 0 {
			e.log.Info("items marked ready", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Pass drains queued items in batches until the queue is empty, bounded by
// maxDrain so one pass cannot run forever.
func (e *Enricher) Pass(ctx context.Context) (int, error) {
	total := 0
	for total < maxDrain {
		items, err := e.store.QueuedItems(ctx, e.batch)
		if err != nil {
			e.record(err)
			return total, err
		}
		if len(items) == 0 {
			e.record(nil)
			return total, nil
		}
		for _, item := range items {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			if err := e.store.MarkReady(ctx, item.ID); err != nil {
				e.record(err)
				return total, err
			}
			total++
		}
		if len(items) < e.batch {
			e.record(nil)
			return total, nil
		}
	}
	e.record(nil)
	return total, nil
}

func (e *Enricher) record(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastRun = time.Now()
	if err != nil {
		e.lastErr = err.Error()
	} else {
		e.lastErr = ""
	}
}
