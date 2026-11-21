// processed_jobs: the Worker job ids already ingested. It is what makes the
// ingest stage idempotent under a lease expiry, a crash between commit and
// `done`, and `harbor replay` (design.md §5 "Job lifecycle").
//
// The row and the item are written in one transaction. Either both land or
// neither does: a missing job row replays the save, and a job row without its
// item loses it silently, which is the one failure the save path must not have.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// ErrInvalidJobID is returned when a job id is blank. An empty id would make
// every blank job look already processed, so it is rejected instead.
var ErrInvalidJobID = errors.New("store: a job needs an id")

// IngestResult is what the ingest stage needs to log and to decide whether to
// report `done`: the item the job landed on, whether that was a merge, and
// whether this job id had been recorded before.
type IngestResult struct {
	Item    model.Item
	Merged  bool
	Already bool
}

// IngestJob writes one Worker job's item and records the job id atomically.
// A job seen before changes nothing and reports Already, which is exactly what
// a re-claim after a crash and a replay after a restore both need.
//
// The item is merged by canonical URL when it has one (design.md §5); a note
// job is always inserted, because two identical notes are two thoughts.
func (s *Store) IngestJob(ctx context.Context, jobID string, in ItemInput) (IngestResult, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return IngestResult{}, ErrInvalidJobID
	}

	var out IngestResult
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var itemID string
		err := tx.QueryRowContext(ctx,
			`SELECT coalesce(item_id, '') FROM processed_jobs WHERE job_id = ?`, jobID).Scan(&itemID)
		switch {
		case err == nil:
			out.Already = true
			if itemID == "" {
				return nil
			}
			// The item may have been purged since; the job is still done, so a
			// missing row is not an error. The caller only logs the result.
			item, err := getItemTx(ctx, tx, itemID)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			out.Item = item
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("looking up job %s: %w", jobID, err)
		}

		item, merged, err := writeItemTx(ctx, tx, in, hasURL(in))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO processed_jobs (job_id, item_id) VALUES (?, ?)`, jobID, item.ID); err != nil {
			return fmt.Errorf("recording job %s: %w", jobID, err)
		}
		out.Item = item
		out.Merged = merged
		return nil
	})
	if err != nil {
		return IngestResult{}, err
	}
	return out, nil
}

// JobIngested reports whether a job id has been recorded. It exists for the
// status endpoint and for tests; ingest itself must use IngestJob so the check
// and the write share a transaction.
func (s *Store) JobIngested(ctx context.Context, jobID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM processed_jobs WHERE job_id = ?`, jobID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("looking up job %s: %w", jobID, err)
	}
	return n > 0, nil
}
