// Counts for the status endpoint. It is deliberately three integers: /api/status
// is the header-less exemption, so it may answer counts and nothing else
// (design.md §3). No query here returns an item, a title or a URL.
package store

import (
	"context"
	"fmt"
)

// Counts is the library at a glance. Items counts live rows; Pending is the
// enrichment queue (queued plus enriching); Failed is enrichment that gave up
// and is waiting for a human.
type Counts struct {
	Items   int
	Pending int
	Failed  int
}

// Counts reads the three numbers in one round trip. A soft-deleted item is in
// none of them: it is not in the library, not queued, and not a failure.
func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `
		SELECT
			coalesce(sum(CASE WHEN deleted_at IS NULL THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN deleted_at IS NULL AND state IN ('queued', 'enriching') THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN deleted_at IS NULL AND state = 'failed' THEN 1 ELSE 0 END), 0)
		FROM items`).Scan(&c.Items, &c.Pending, &c.Failed)
	if err != nil {
		return Counts{}, fmt.Errorf("counts: %w", err)
	}
	return c, nil
}
