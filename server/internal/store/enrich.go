// The enrichment queue's read side. In phase 2 the stage has no model work to
// do: it picks queued items in priority order and marks them ready, so the
// column, the index and the loop shape are real when phase 3 fills them in
// (design.md §5 "Job lifecycle").
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// QueuedItems is one enrichment batch: the highest priority first, then the
// earliest due, then oldest saved so a backlog drains in order. A NULL
// next_attempt_at is due now.
func (s *Store) QueuedItems(ctx context.Context, limit int) ([]model.Item, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM items
		WHERE state = 'queued' AND deleted_at IS NULL
		ORDER BY priority DESC, next_attempt_at IS NULL DESC, next_attempt_at ASC, saved_at ASC, seq ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("queued items: %w", err)
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

// MarkReady closes the enrichment stage for an item. It is idempotent: marking
// a ready item ready again is a no-op rather than an error.
func (s *Store) MarkReady(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE items
		SET state = 'ready', enriched_at = ?, next_attempt_at = NULL, last_error = ''
		WHERE id = ? AND state = 'queued'`, stamp(time.Now().UTC()), id); err != nil {
		return fmt.Errorf("marking %s ready: %w", id, err)
	}
	// Zero rows means it was already ready, already failed, or gone; none of
	// those is an error to the caller.
	return nil
}
