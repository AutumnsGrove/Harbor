// The attention list: items whose enrichment gave up. It reads the item rows,
// not the logs, so a restart does not hide a failure (design.md §6).
package store

import (
	"context"
	"fmt"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// FailedItems is the Needs attention strip: failed, live items, newest saved
// first.
func (s *Store) FailedItems(ctx context.Context, limit int) ([]model.Item, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM items
		WHERE state = 'failed' AND deleted_at IS NULL
		ORDER BY saved_at DESC, seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed items: %w", err)
	}
	defer rows.Close()

	items := make([]model.Item, 0, limit)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// RetryItem puts a *failed* item back in the queue immediately: attempts reset,
// no backoff, and the old reason cleared so a successful pass leaves no stale
// trace. An item that is queued, enriching or ready is left alone — retrying a
// ready item would re-enrich it and overwrite a summary that already
// succeeded. A soft-deleted or missing item is ErrNotFound.
func (s *Store) RetryItem(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE items
		SET state = 'queued', attempts = 0, next_attempt_at = NULL, last_error = ''
		WHERE id = ? AND deleted_at IS NULL AND state = 'failed'`, id)
	if err != nil {
		return fmt.Errorf("retrying %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("retrying %s: %w", id, err)
	}
	if affected == 0 {
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE id = ? AND deleted_at IS NULL`, id).Scan(&exists)
		if err != nil {
			return fmt.Errorf("retrying %s: %w", id, err)
		}
		if exists == 0 {
			return ErrNotFound
		}
		// Queued, enriching or ready: already on its way or already done, so
		// retrying is a no-op rather than a re-enrichment.
		return nil
	}
	return nil
}
