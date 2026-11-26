// Import runs: the bookkeeping that makes a cancelled import resumable.
//
// Import is already idempotent by canonical URL, so a resume does not need to
// know which rows landed — it needs to know how far the file was read. That
// counter is all this table holds, and it is keyed by the file's own sha256 so
// a second file cannot inherit the first one's progress.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// Import run states. A run that is running or cancelled can be resumed; a done
// one is finished and starting it again is a fresh import (which merges).
const (
	ImportRunning   = "running"
	ImportCancelled = "cancelled"
	ImportDone      = "done"
)

// StartImportRun opens a run for this file, resuming the most recent unfinished
// one with the same sha256 when there is one.
//
// The resume is per file, not per row: importing the same file again after a
// crash continues where it stopped instead of re-reading 1,400 rows it already
// merged. Different content is a different run, because the checkpoint would
// mean something else.
func (s *Store) StartImportRun(ctx context.Context, fileSHA256 string, counts string) (model.ImportRun, bool, error) {
	if fileSHA256 == "" {
		return model.ImportRun{}, false, errors.New("store: an import run needs the file's sha256")
	}
	if counts == "" {
		counts = "{}"
	}

	existing, err := s.latestUnfinishedImportRun(ctx, fileSHA256)
	switch {
	case err == nil:
		// The counts of a resumed run are the ones it had reached; the caller
		// adds to them from the checkpoint.
		if _, err := s.db.ExecContext(ctx,
			`UPDATE import_runs SET state = ? WHERE id = ? AND state = ?`,
			ImportRunning, existing.ID, ImportCancelled); err != nil {
			return model.ImportRun{}, false, fmt.Errorf("resuming import run %s: %w", existing.ID, err)
		}
		existing.State = ImportRunning
		return existing, true, nil
	case !errors.Is(err, ErrNotFound):
		return model.ImportRun{}, false, err
	}

	run := model.ImportRun{
		ID:         uuid.NewString(),
		FileSHA256: fileSHA256,
		State:      ImportRunning,
		CountsJSON: counts,
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO import_runs (id, file_sha256, state, checkpoint, counts_json, started_at)
		 VALUES (?, ?, ?, 0, ?, ?)`,
		run.ID, run.FileSHA256, run.State, run.CountsJSON, Stamp(time.Now().UTC())); err != nil {
		return model.ImportRun{}, false, fmt.Errorf("starting an import run: %w", err)
	}
	stored, err := s.ImportRunByID(ctx, run.ID)
	if err != nil {
		return model.ImportRun{}, false, err
	}
	return stored, false, nil
}

// ImportRunByID reads one run.
func (s *Store) ImportRunByID(ctx context.Context, id string) (model.ImportRun, error) {
	return scanImportRun(s.db.QueryRowContext(ctx,
		`SELECT id, file_sha256, state, checkpoint, counts_json, started_at, finished_at
		 FROM import_runs WHERE id = ?`, id))
}

// latestUnfinishedImportRun is the run a resume would continue: the newest one
// for this file that is not done. rowid breaks a tie inside the same
// nanosecond, which only a test can produce.
func (s *Store) latestUnfinishedImportRun(ctx context.Context, fileSHA256 string) (model.ImportRun, error) {
	return scanImportRun(s.db.QueryRowContext(ctx,
		`SELECT id, file_sha256, state, checkpoint, counts_json, started_at, finished_at
		 FROM import_runs WHERE file_sha256 = ? AND state <> ?
		 ORDER BY started_at DESC, rowid DESC LIMIT 1`, fileSHA256, ImportDone))
}

// LatestImportRun is what the Status page and Settings → Import report: the most
// recent run of any state.
func (s *Store) LatestImportRun(ctx context.Context) (model.ImportRun, error) {
	return scanImportRun(s.db.QueryRowContext(ctx,
		`SELECT id, file_sha256, state, checkpoint, counts_json, started_at, finished_at
		 FROM import_runs ORDER BY started_at DESC, rowid DESC LIMIT 1`))
}

// CheckpointImportRun records how far the run has read, so a crash or a Ctrl-C
// costs nothing but the rows in flight. Writing counts with it keeps the report
// honest after a resume.
func (s *Store) CheckpointImportRun(ctx context.Context, id string, records int, counts string) error {
	if records < 0 {
		return fmt.Errorf("store: import run %s checkpoint %d is negative", id, records)
	}
	if counts == "" {
		counts = "{}"
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE import_runs SET checkpoint = ?, counts_json = ? WHERE id = ?`,
		records, counts, id)
	if err != nil {
		return fmt.Errorf("checkpointing import run %s: %w", id, err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// FinishImportRun closes the run: done on success, cancelled on a stop. The
// final checkpoint is written in the same statement, so the row never says
// "done" while pointing at an older position.
func (s *Store) FinishImportRun(ctx context.Context, id, state string, records int, counts string) error {
	if state != ImportDone && state != ImportCancelled {
		return fmt.Errorf("store: %q is not a final import state", state)
	}
	if counts == "" {
		counts = "{}"
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE import_runs SET state = ?, checkpoint = ?, counts_json = ?, finished_at = ?
		 WHERE id = ?`, state, records, counts, Stamp(time.Now().UTC()), id)
	if err != nil {
		return fmt.Errorf("finishing import run %s: %w", id, err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

func scanImportRun(row rowScanner) (model.ImportRun, error) {
	var (
		run      model.ImportRun
		started  string
		finished sql.NullString
	)
	err := row.Scan(&run.ID, &run.FileSHA256, &run.State, &run.Checkpoint, &run.CountsJSON, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ImportRun{}, ErrNotFound
	}
	if err != nil {
		return model.ImportRun{}, err
	}
	if run.StartedAt, err = parseStamp(started); err != nil {
		return model.ImportRun{}, fmt.Errorf("import run %s started_at: %w", run.ID, err)
	}
	if finished.Valid {
		at, err := parseStamp(finished.String)
		if err != nil {
			return model.ImportRun{}, fmt.Errorf("import run %s finished_at: %w", run.ID, err)
		}
		run.FinishedAt = &at
	}
	return run, nil
}
