// Counts for the status endpoint. It is deliberately three integers: /api/status
// is the header-less exemption, so it may answer counts and nothing else
// (design.md §3). No query here returns an item, a title or a URL.
package store

import (
	"context"
	"fmt"
	"time"
)

// Counts is the library at a glance. Items counts live rows; Pending is the
// enrichment queue (queued plus enriching); Failed is enrichment that gave up
// and is waiting for a human.
type Counts struct {
	Items   int
	Pending int
	Failed  int

	// SavedThisWeek is live items saved on or after the instant the caller
	// passed in. The caller owns the clock, so the query stays a plain
	// comparison against the stored saved_at and the window is testable. The
	// bound is formatted with stamp: saved_at is text, and the driver's own
	// time binding ("2006-01-02 15:04:05 +0000 UTC") compares wrong against it
	// — `'T' > ' '` at position 10, so a whole calendar day leaks in.
	SavedThisWeek int
}

// Counts reads the three numbers in one round trip. A soft-deleted item is in
// none of them: it is not in the library, not queued, and not a failure.
func (s *Store) Counts(ctx context.Context, since time.Time) (Counts, error) {
	var c Counts
	err := s.db.QueryRowContext(ctx, `
		SELECT
			coalesce(sum(CASE WHEN deleted_at IS NULL THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN deleted_at IS NULL AND state IN ('queued', 'enriching') THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN deleted_at IS NULL AND state = 'failed' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN deleted_at IS NULL AND saved_at >= ? THEN 1 ELSE 0 END), 0)
		FROM items`, stamp(since)).Scan(&c.Items, &c.Pending, &c.Failed, &c.SavedThisWeek)
	if err != nil {
		return Counts{}, fmt.Errorf("counts: %w", err)
	}
	return c, nil
}
