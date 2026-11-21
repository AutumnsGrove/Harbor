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

// Processor does the work for one item. enrich.Enricher implements it; the
// phase-2 shell passes nil, which just marks items ready.
type Processor interface {
	Process(ctx context.Context, item model.Item) error
}

// Enricher drains the queued items in priority order. With a Processor it runs
// the real pipeline; without one it is phase 2's shell, which flips items to
// ready so the state column and the pending count are honest (design.md §5).
type Enricher struct {
	store     EnrichStore
	processor Processor
	log       *slog.Logger
	cadence   time.Duration
	batch     int
	failed    int

	mu      sync.Mutex
	lastRun time.Time
	lastErr string
}

// NewEnricher builds the stage: every five seconds, fifty items at a time. A
// nil processor is the phase-2 shell.
func NewEnricher(s EnrichStore, processor Processor, log *slog.Logger) *Enricher {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Enricher{store: s, processor: processor, log: log, cadence: 5 * time.Second, batch: 50}
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
			e.log.Info("enrichment pass finished", "items", n, "failed", e.failed)
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
			if e.processor != nil {
				// A failed item has already recorded its retry on the row, so
				// the next QueuedItems call will not return it; the batch keeps
				// going regardless (plans/03 test 14).
				if err := e.processor.Process(ctx, item); err != nil {
					e.failed++
					e.log.Error("enrichment item failed", "item", item.ID, "error", err)
				}
			} else if err := e.store.MarkReady(ctx, item.ID); err != nil {
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
