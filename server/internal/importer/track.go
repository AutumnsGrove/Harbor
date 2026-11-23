// RunTracked is one import as a row in import_runs: the protocol both
// interfaces speak.
//
// The CLI and Settings → Import do the same three bookkeeping things around a
// run, and getting any of them wrong loses a row: resume from the checkpoint,
// write the checkpoint on a context an interrupt cannot kill, and close the run
// as done or cancelled. They live here once (plans/05, "Resumability").
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// DefaultCheckpointEvery is how often a run records how far it has read when the
// caller has no opinion. Low enough that a Ctrl-C costs at most this many rows
// of re-reading, high enough that the checkpoint write is not the run's cost.
const DefaultCheckpointEvery = 100

// Tracked is what a run left behind for the caller to report.
type Tracked struct {
	// RunID is the import_runs row. It is what a status poll reads.
	RunID string
	// From is how many records an earlier run of this file had already read;
	// the run started behind them rather than at the beginning.
	From int
	// Resumed says the run continued a cancelled one.
	Resumed bool
	// Stopped says the context was cancelled mid-run. It is a controlled stop,
	// not a failure: the run row says cancelled and the same file resumes it.
	Stopped bool
}

// RunTracked imports every row through Run, with the resume bookkeeping around
// it. fileSHA256 is the run's key, so the caller hashes the file, not this.
//
// A cancellation is not an error: it comes back as Tracked.Stopped, because a
// human stopping an import is the intended way to interrupt one. Any other
// error is the run's own, and the run row is still closed — as done, the same as
// the CLI has always done, because the rows it did read landed and re-running
// the file merges.
//
// A dry run does not belong here: it writes nothing, import_runs included, so
// the caller runs Options{DryRun: true} through Run directly.
func RunTracked(ctx context.Context, s *store.Store, fileSHA256 string, src Source, opts Options) (Report, Tracked, error) {
	run, resumed, err := s.StartImportRun(ctx, fileSHA256, "{}")
	if err != nil {
		return Report{}, Tracked{}, err
	}

	tracked := Tracked{RunID: run.ID, Resumed: resumed}
	if resumed {
		// The checkpoint is rows read, and a resume skips exactly those: the
		// merge rule makes re-applying one harmless, but re-reading them all
		// would make a resume as slow as the run it continues.
		tracked.From = run.Checkpoint
		opts.SkipRows = run.Checkpoint
	}
	if opts.CheckpointEvery <= 0 {
		opts.CheckpointEvery = DefaultCheckpointEvery
	}
	if opts.OnStart != nil {
		opts.OnStart(tracked.Resumed, tracked.From)
	}

	// Bookkeeping writes must survive the interrupt that stops the import:
	// Ctrl-C cancels ctx, and a checkpoint (or the final state) written on it
	// would fail, leaving the row saying "running" at an older position than
	// the one the operator was told.
	bookkeeping := context.WithoutCancel(ctx)
	opts.OnCheckpoint = func(records int, report Report) error {
		encoded, err := json.Marshal(report)
		if err != nil {
			return fmt.Errorf("encoding the checkpoint: %w", err)
		}
		return s.CheckpointImportRun(bookkeeping, run.ID, records, string(encoded))
	}

	report, err := Run(ctx, s, src, opts)
	tracked.Stopped = errors.Is(err, context.Canceled)

	// Close the run either way: done if the file was read to the end, cancelled
	// if a human (or a shutdown) stopped it, so the next attempt resumes rather
	// than restarts.
	final := store.ImportDone
	if tracked.Stopped {
		final = store.ImportCancelled
	}
	if finishErr := finishRun(bookkeeping, s, run.ID, final, tracked.From+report.Total, report); finishErr != nil {
		return report, tracked, finishErr
	}
	if tracked.Stopped {
		err = nil
	}
	return report, tracked, err
}

// finishRun writes the run's final state. bookkeeping already survives the
// cancellation that got us here.
func finishRun(ctx context.Context, s *store.Store, id, state string, records int, report Report) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encoding the report: %w", err)
	}
	return s.FinishImportRun(ctx, id, state, records, string(encoded))
}
