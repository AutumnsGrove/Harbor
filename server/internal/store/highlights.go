// Highlights: the imported ones, and the read the item page needs.
//
// Phase 8 owns anchored reader highlights. An imported highlight has no text to
// anchor against — the page has not been extracted yet, and the export does not
// say which paragraph it came from — so its anchor_json says only that it is
// imported and in what order. That is enough to render it beside the item and
// enough to re-import the file without duplicating or reordering anything.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/google/uuid"
)

// importedAnchorSource is the anchor_json marker that separates the file's
// highlights from the owner's: a re-import may replace the former and never the
// latter.
const importedAnchorSource = "imported"

// importedAnchorJSON is what an imported highlight's anchor_json looks like.
// It is a fixed shape so that the store, not the caller, owns the format.
func importedAnchorJSON(index int) string {
	return fmt.Sprintf(`{"source":%q,"index":%d}`, importedAnchorSource, index)
}

// ReplaceImportedHighlights makes the item's imported highlights exactly quotes,
// in that order. Reader highlights are untouched (design.md §5: highlights are
// the part the owner cannot recreate, and that cuts both ways).
//
// It is idempotent in the strict sense: when the item already carries exactly
// these quotes, nothing is written and the rows keep their ids and timestamps,
// so importing the same file twice leaves the library identical.
func (s *Store) ReplaceImportedHighlights(ctx context.Context, itemID string, quotes []string) error {
	want := make([]string, 0, len(quotes))
	for _, quote := range quotes {
		if quote = strings.TrimSpace(quote); quote != "" {
			want = append(want, quote)
		}
	}

	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := getItemTx(ctx, tx, itemID); err != nil {
			return err
		}
		have, err := importedQuotesTx(ctx, tx, itemID)
		if err != nil {
			return err
		}
		if equalQuotes(have, want) {
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM highlights WHERE item_id = ? AND json_extract(anchor_json, '$.source') = ?`,
			itemID, importedAnchorSource); err != nil {
			return fmt.Errorf("clearing imported highlights for %s: %w", itemID, err)
		}

		// The cell's order is the order they were highlighted in, and the
		// created_at stamps are what a read sorts on, so they carry the index.
		base := time.Now().UTC()
		for i, quote := range want {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO highlights (id, item_id, quote, note, color, anchor_json, detached, created_at)
				 VALUES (?, ?, ?, '', ?, ?, 0, ?)`,
				uuid.NewString(), itemID, quote, string(model.ColorAmber), importedAnchorJSON(i),
				stamp(base.Add(time.Duration(i)*time.Microsecond))); err != nil {
				return fmt.Errorf("inserting imported highlight %d for %s: %w", i, itemID, err)
			}
		}
		return nil
	})
}

// HighlightsForItem is the item page's read, in the order they were made.
func (s *Store) HighlightsForItem(ctx context.Context, itemID string) ([]model.Highlight, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, item_id, quote, note, color, anchor_json, detached, created_at
		FROM highlights WHERE item_id = ?
		ORDER BY created_at, COALESCE(json_extract(anchor_json, '$.index'), 0), id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("highlights for %s: %w", itemID, err)
	}
	defer rows.Close()

	var out []model.Highlight
	for rows.Next() {
		var (
			highlight model.Highlight
			color     string
			detached  int
			created   string
		)
		if err := rows.Scan(&highlight.ID, &highlight.ItemID, &highlight.Quote, &highlight.Note,
			&color, &highlight.AnchorJSON, &detached, &created); err != nil {
			return nil, err
		}
		highlight.Color = model.HighlightColor(color)
		highlight.Detached = detached != 0
		if highlight.CreatedAt, err = parseStamp(created); err != nil {
			return nil, fmt.Errorf("highlight %s created_at: %w", highlight.ID, err)
		}
		out = append(out, highlight)
	}
	return out, rows.Err()
}

// importedQuotesTx reads the imports' quotes in order; equalQuotes compares them
// to what a re-import would write.
func importedQuotesTx(ctx context.Context, tx *sql.Tx, itemID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT quote FROM highlights
		WHERE item_id = ? AND json_extract(anchor_json, '$.source') = ?
		ORDER BY created_at, COALESCE(json_extract(anchor_json, '$.index'), 0), id`,
		itemID, importedAnchorSource)
	if err != nil {
		return nil, fmt.Errorf("reading imported highlights for %s: %w", itemID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var quote string
		if err := rows.Scan(&quote); err != nil {
			return nil, err
		}
		out = append(out, quote)
	}
	return out, rows.Err()
}

func equalQuotes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
