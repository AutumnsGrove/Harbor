// Package queue is the potato's side of the two queues design.md §5 keeps
// apart: the **ingest** stage (claim a Worker job, make it durable, report
// done) and, from phase 3, the **enrichment** stage (work driven by the item
// row). Ingest never touches a model and never fetches a page.
//
// Three rules shape this package:
//
//   - `done` is sent only after the item and its job id are committed together
//     (store.IngestJob), so a crash re-claims and replays a no-op.
//   - claim is limited to the kinds this build can process. A job that cannot
//     be ingested is never leased and then failed.
//   - a bad job is reported failed and the batch keeps draining. One unparseable
//     link must not hold up the nine good saves behind it.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/AutumnsGrove/Harbor/server/internal/canon"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
)

// claimBatch is how many jobs one claim asks for. The Worker caps at 50, and
// draining happens in a loop, so this only decides the round-trip size.
const claimBatch = 10

// claimKinds is what this build can ingest. Uploads arrive with phase 6 (blobs,
// media rows, hashing); claiming them now would park real saves as failures.
var claimKinds = []string{workerclient.KindLink, workerclient.KindNote}

// maxDrain bounds one drain pass. The queue is expected to empty well before
// this; the cap exists so a misbehaving Worker cannot keep one poll running
// forever (plans/README.md invariant 7: no unbounded loop).
const maxDrain = 500

// Worker is the part of the Worker API ingest needs. workerclient.Client
// satisfies it; tests use a hand-written fake.
type Worker interface {
	Claim(ctx context.Context, limit int, kinds []string) ([]workerclient.Job, error)
	Done(ctx context.Context, id string) error
	Failed(ctx context.Context, id, reason string) error
}

// ItemStore is the part of store.Store ingest needs.
type ItemStore interface {
	IngestJob(ctx context.Context, jobID string, in store.ItemInput) (store.IngestResult, error)
}

// Stats is one drain pass, for the log line and the status page.
type Stats struct {
	Claimed  int
	Ingested int
	Merged   int
	Already  int
	Failed   int
}

// Ingestor turns claimed jobs into items. It is safe to call Ingest
// concurrently; the store serialises writes and the Worker records a job id
// once.
type Ingestor struct {
	worker Worker
	store  ItemStore
	log    *slog.Logger

	// newID and now are fields so tests can make the result deterministic.
	newID func() string
	now   func() time.Time

	// doneAttempts is how many times `done` is retried before the job is left
	// for the next claim. The item is already durable at that point, so a retry
	// reprocesses nothing (plans/02 test 8).
	doneAttempts int
	doneBase     time.Duration
}

// NewIngestor wires the real Worker client and store. Every field below is a
// default the tests override in-package; production never changes them.
func NewIngestor(w Worker, s ItemStore, log *slog.Logger) *Ingestor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Ingestor{
		worker:       w,
		store:        s,
		log:          log,
		newID:        uuid.NewString,
		now:          time.Now,
		doneAttempts: 3,
		doneBase:     200 * time.Millisecond,
	}
}

// Ingest makes one job durable and reports done. The item write happens before
// the network call; a `done` that fails is retried, and the job is left claimed
// rather than failed, because the save itself succeeded.
func (i *Ingestor) Ingest(ctx context.Context, job workerclient.Job) (store.IngestResult, error) {
	in, err := i.itemInput(job)
	if err != nil {
		i.reportFailed(ctx, job, err)
		return store.IngestResult{}, err
	}

	res, err := i.store.IngestJob(ctx, job.ID, in)
	if err != nil {
		i.reportFailed(ctx, job, err)
		return store.IngestResult{}, err
	}

	if err := i.done(ctx, job.ID); err != nil {
		// The item is committed and the job id is recorded, so the next claim
		// reprocesses nothing: it only retries this call.
		return res, fmt.Errorf("job %s was ingested but not marked done: %w", job.ID, err)
	}

	level := slog.LevelInfo
	if res.Already {
		level = slog.LevelDebug
	}
	i.log.Log(ctx, level, "ingested job",
		"job", job.ID,
		"item", res.Item.ID,
		"kind", job.Kind,
		"source", job.Source,
		"merged", res.Merged,
		"already", res.Already,
	)
	return res, nil
}

// Drain claims and ingests until the Worker answers with nothing. A job that
// fails to ingest is reported failed and counted; it does not stop the batch.
// The returned error is a claim failure or a cancelled context, never one bad
// job.
func (i *Ingestor) Drain(ctx context.Context) (Stats, error) {
	var stats Stats
	for stats.Claimed < maxDrain {
		jobs, err := i.worker.Claim(ctx, claimBatch, claimKinds)
		if err != nil {
			return stats, err
		}
		if len(jobs) == 0 {
			return stats, nil
		}
		for _, job := range jobs {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			stats.Claimed++
			res, err := i.Ingest(ctx, job)
			stats.count(res, err)
			if err != nil {
				i.log.Error("ingest failed", "job", job.ID, "error", err)
			}
		}
		if len(jobs) < claimBatch {
			// A short batch means the queue is empty; asking again costs a
			// round trip and returns nothing.
			return stats, nil
		}
	}
	i.log.Warn("drain reached its per-pass cap", "cap", maxDrain)
	return stats, nil
}

// IngestAll applies a fixed list of jobs, which is what `harbor replay` does.
// Idempotence comes from the store, so replaying an overlapping window is safe.
func (i *Ingestor) IngestAll(ctx context.Context, jobs []workerclient.Job) (Stats, error) {
	var stats Stats
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		stats.Claimed++
		res, err := i.Ingest(ctx, job)
		stats.count(res, err)
		if err != nil {
			i.log.Error("replay failed", "job", job.ID, "error", err)
		}
	}
	return stats, nil
}

// count folds one Ingest result into the pass. An item and a failure are not
// exclusive: the row can be durable while its `done` call still failed, and the
// log should say so rather than pretend the save did not happen.
func (s *Stats) count(res store.IngestResult, err error) {
	switch {
	case res.Already:
		s.Already++
	case res.Item.ID != "":
		s.Ingested++
		if res.Merged {
			s.Merged++
		}
	}
	if err != nil {
		s.Failed++
	}
}

// itemInput maps the wire contract to the item row. This is the only place the
// two vocabularies meet, so a new job field has exactly one place to land.
func (i *Ingestor) itemInput(job workerclient.Job) (store.ItemInput, error) {
	in := store.ItemInput{
		ID:       i.newID(),
		Priority: 0, // interactive: a door save outranks an import backlog
	}

	source, err := sourceFor(job.Source)
	if err != nil {
		return in, err
	}
	in.Source = source

	if job.Note != nil {
		in.Note = *job.Note
	}
	if job.Title != nil {
		in.Title = strings.TrimSpace(*job.Title)
	}
	// The Worker's created_at is when the human saved it. Keeping it makes a
	// save made while the potato was asleep land at its real position.
	if job.CreatedAt > 0 {
		in.CreatedAt = time.Unix(job.CreatedAt, 0).UTC()
		in.SavedAt = in.CreatedAt
	}

	switch job.Kind {
	case workerclient.KindNote:
		in.Kind = model.KindNote
		if strings.TrimSpace(in.Note) == "" {
			return in, errors.New("note job carries no note")
		}
	case workerclient.KindLink:
		if job.Link == nil || strings.TrimSpace(*job.Link) == "" {
			return in, errors.New("link job carries no link")
		}
		raw := strings.TrimSpace(*job.Link)
		canonical, err := canon.CanonicalURL(raw)
		if err != nil {
			return in, fmt.Errorf("link %q: %w", raw, err)
		}
		in.URL = &canonical
		in.URLOriginal = raw
		in.Kind = model.KindArticle // phase 3 classifies; never "unknown"
		if in.Title == "" {
			// The exit demo's fallback: the URL, until enrichment brings a title.
			in.Title = canonical
		}
	default:
		return in, fmt.Errorf("job kind %q cannot be ingested yet", job.Kind)
	}
	return in, nil
}

// done retries a transient failure. A 401 is not transient: retrying with the
// same token cannot help, so it is left for the next claim after the operator
// fixes the token.
func (i *Ingestor) done(ctx context.Context, id string) error {
	var err error
	for attempt := 1; attempt <= i.doneAttempts; attempt++ {
		if err = i.worker.Done(ctx, id); err == nil {
			return nil
		}
		if errors.Is(err, workerclient.ErrUnauthorized) || ctx.Err() != nil {
			return err
		}
		if attempt < i.doneAttempts {
			if !sleep(ctx, i.doneBase<<(attempt-1)) {
				return ctx.Err()
			}
		}
	}
	return err
}

// reportFailed tells the Worker why a job could not be ingested. A failure to
// report is logged and swallowed: the next claim retries the whole job, and
// there is nothing better to do from here.
func (i *Ingestor) reportFailed(ctx context.Context, job workerclient.Job, cause error) {
	reason := cause.Error()
	if len(reason) > 500 {
		reason = reason[:500]
	}
	// A cancelled context is ours, not the job's: reporting it would park a
	// perfectly good save because the container is shutting down.
	if ctx.Err() != nil {
		return
	}
	if err := i.worker.Failed(ctx, job.ID, reason); err != nil {
		i.log.Error("could not report a failed job", "job", job.ID, "error", err)
	}
}

func sourceFor(source string) (model.Source, error) {
	switch source {
	case workerclient.SourceTelegram:
		return model.SourceTelegram, nil
	case workerclient.SourceShortcut:
		return model.SourceShortcut, nil
	default:
		return "", fmt.Errorf("unknown job source %q", source)
	}
}

// sleep waits for d or until the context ends. It reports whether the full
// wait happened.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
