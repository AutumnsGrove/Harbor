// The ingest loop: poll the Worker, drain the queue, back off when the network
// is down, and stay visible when it is not working. It is a goroutine with a
// context and nothing else — the potato is a client (design.md §4).
package queue

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
)

const (
	// PollCadence is the idle poll interval design.md §5 specifies.
	PollCadence = 30 * time.Second

	// Backoff bounds for a failing Worker. The floor is two seconds, so a
	// Worker that is down does not get hammered; the ceiling keeps a recovered
	// Worker from waiting absurdly long.
	backoffBase = 2 * time.Second
	backoffMax  = 5 * time.Minute
)

// Backoff is the delay after the given consecutive failure. It doubles from
// backoffBase and never exceeds backoffMax. A success resets it by passing 1.
func Backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := backoffBase
	for n := 1; n < attempt; n++ {
		if delay >= backoffMax {
			break
		}
		delay *= 2
	}
	if delay > backoffMax {
		return backoffMax
	}
	return delay
}

// Status is the loop's health signal. /api/status reads it, and the plan's
// "loop stopped" warning comes from LastPollAt going stale.
type Status struct {
	LastPollAt time.Time
	LastOKAt   time.Time
	NextPollAt time.Time
	LastError  string
	Paused     bool
	LastStats  Stats
}

// Loop runs Drain on a cadence, or immediately when Refresh is called (the UI
// opening is the trigger design.md §5 asks for).
type Loop struct {
	ingestor *Ingestor
	log      *slog.Logger

	cadence    time.Duration
	maxBackoff time.Duration
	now        func() time.Time
	jitter     func(time.Duration) time.Duration
	refresh    chan struct{}

	mu      sync.Mutex
	status  Status
	attempt int
}

// NewLoop builds the production loop: 30 s cadence, jittered, with the
// exponential backoff above.
func NewLoop(ing *Ingestor) *Loop {
	log := slog.New(slog.DiscardHandler)
	if ing != nil && ing.log != nil {
		log = ing.log
	}
	return &Loop{
		ingestor:   ing,
		log:        log,
		cadence:    PollCadence,
		maxBackoff: backoffMax,
		now:        time.Now,
		jitter:     defaultJitter,
		refresh:    make(chan struct{}, 1),
	}
}

// Refresh asks for a poll now. It never blocks and never stacks: a second call
// while one is pending is the same request.
func (l *Loop) Refresh() {
	select {
	case l.refresh <- struct{}{}:
	default:
	}
}

// Status returns a copy, so a handler cannot mutate the loop's state.
func (l *Loop) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// Run is the goroutine. It polls once immediately — a restart must not wait a
// full cadence while saves sit in the queue — then on the cadence or a Refresh.
func (l *Loop) Run(ctx context.Context) {
	if l.ingestor == nil {
		l.log.Error("ingest loop has no ingestor; not starting")
		return
	}
	l.log.Info("ingest loop started", "cadence", l.cadence.String())
	defer l.log.Info("ingest loop stopped")

	for {
		delay := l.step(ctx)
		if ctx.Err() != nil {
			return
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-l.refresh:
			timer.Stop()
		}
	}
}

// step is one poll. It returns how long to wait before the next one, so the
// backoff policy is testable without sleeping.
func (l *Loop) step(ctx context.Context) time.Duration {
	started := l.now()
	stats, err := l.ingestor.Drain(ctx)
	finished := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()
	l.status.LastPollAt = started
	l.status.LastStats = stats

	if err == nil {
		l.attempt = 0
		l.status.LastOKAt = finished
		l.status.LastError = ""
		l.status.Paused = false
		delay := l.jitter(l.cadence)
		l.status.NextPollAt = finished.Add(delay)
		if stats.Claimed > 0 {
			l.log.Info("ingest poll",
				"claimed", stats.Claimed,
				"ingested", stats.Ingested,
				"merged", stats.Merged,
				"already", stats.Already,
				"failed", stats.Failed,
			)
		}
		return delay
	}

	if ctx.Err() != nil {
		// Shutdown is not a Worker failure; leave the last good status alone.
		return 0
	}

	l.status.LastError = err.Error()
	if errors.Is(err, workerclient.ErrUnauthorized) {
		// A bad or revoked token cannot be retried into working, and a hot loop
		// would just hammer the Worker. Poll slowly and say so every time.
		l.status.Paused = true
		delay := l.jitter(l.cadence)
		l.status.NextPollAt = finished.Add(delay)
		l.log.Error("worker rejected the potato token; polling paused", "error", err)
		return delay
	}

	l.status.Paused = false
	l.attempt++
	delay := l.jitter(Backoff(l.attempt))
	if limit := l.jitter(l.maxBackoff); delay > limit {
		delay = limit
	}
	l.status.NextPollAt = finished.Add(delay)
	l.log.Error("ingest poll failed", "error", err, "attempt", l.attempt, "retry_in", delay.String())
	return delay
}

// defaultJitter spreads a cadence by up to 25%, so ten potatoes (or one potato
// restarting) do not line up on the same second. The cap is applied to the base.
func defaultJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d + time.Duration(rand.Int63n(int64(d/4)+1))
}
