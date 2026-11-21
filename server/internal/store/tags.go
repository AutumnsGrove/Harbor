// Tags: the one write path the composer, the importer (phase 5) and the
// enrichment prompt all share (plans/03 test 20). It normalises, resolves
// aliases left by a merge, reuses the canonical tag, and keeps items.tags_text
// and the FTS row in the same transaction as the link.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// maxAliasDepth bounds an alias walk. The table is a redirect map, not a graph,
// but a hand-edited row should fail loudly instead of looping.
const maxAliasDepth = 8

// NormaliseTag is the canonical spelling: surrounding space gone, whitespace
// collapsed, lowercased. Unicode and emoji are left alone; they are real tags.
func NormaliseTag(raw string) string {
	return strings.ToLower(strings.Join(strings.Fields(raw), " "))
}

// SetItemTags replaces one source's tags for an item and returns the canonical
// names, sorted. AI tags are replaced on re-enrichment; manual tags are never
// touched by it (phase 9 adds the manual editor).
func (s *Store) SetItemTags(ctx context.Context, itemID string, names []string, source model.TagSource) ([]string, error) {
	if !source.Valid() {
		return nil, fmt.Errorf("store: %q is not a tag source", source)
	}
	var canonical []string
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		item, err := getItemTx(ctx, tx, itemID)
		if err != nil {
			return err
		}
		before := item

		canonical, err = replaceTagsTx(ctx, tx, itemID, names, source)
		if err != nil {
			return err
		}
		if err := refreshTagsText(ctx, tx, item.Seq); err != nil {
			return err
		}
		fresh, err := getItemTx(ctx, tx, itemID)
		if err != nil {
			return err
		}
		// A soft-deleted item is not in the index; changing its tags must not
		// make it searchable (Restore re-indexes).
		if before.DeletedAt == nil {
			if err := ftsDelete(ctx, tx, before); err != nil {
				return err
			}
			if err := ftsInsert(ctx, tx, fresh); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// TagsForItemWithSource is the UI's read of who assigned a tag. Phase 9 uses
// it; phase 3 only needs the names, which TagsForItem already returns.
func (s *Store) TagsForItemWithSource(ctx context.Context, itemID string) (map[string]model.TagSource, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.name, it.source FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE it.item_id = ? ORDER BY t.name`,
		itemID)
	if err != nil {
		return nil, fmt.Errorf("tags for %s: %w", itemID, err)
	}
	defer rows.Close()
	out := map[string]model.TagSource{}
	for rows.Next() {
		var name, source string
		if err := rows.Scan(&name, &source); err != nil {
			return nil, err
		}
		out[name] = model.TagSource(source)
	}
	return out, rows.Err()
}

// replaceTagsTx removes the source's existing links and unions in the new
// names. The delete is per-source so AI re-enrichment cannot erase a manual tag.
func replaceTagsTx(ctx context.Context, tx *sql.Tx, itemID string, names []string, source model.TagSource) ([]string, error) {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM item_tags WHERE item_id = ? AND source = ?`, itemID, string(source)); err != nil {
		return nil, fmt.Errorf("clearing %s tags for %s: %w", source, itemID, err)
	}
	return ensureTagsTx(ctx, tx, itemID, names, source)
}

// ensureTagsTx unions normalised names into the item, creating tags as needed.
// It returns the canonical names sorted and deduplicated by tag id, because two
// spellings can resolve to the same alias target.
func ensureTagsTx(ctx context.Context, tx *sql.Tx, itemID string, names []string, source model.TagSource) ([]string, error) {
	type resolved struct {
		id   int64
		name string
	}
	seen := map[int64]string{}
	for _, raw := range names {
		name := NormaliseTag(raw)
		if name == "" {
			continue
		}
		id, canonical, err := resolveTagTx(ctx, tx, name)
		if err != nil {
			return nil, err
		}
		seen[id] = canonical
	}

	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return seen[ids[i]] < seen[ids[j]] })

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO item_tags (item_id, tag_id, source) VALUES (?, ?, ?)
			 ON CONFLICT(item_id, tag_id) DO UPDATE SET source = excluded.source
			 WHERE item_tags.source <> 'manual'`,
			itemID, id, string(source)); err != nil {
			return nil, fmt.Errorf("linking tag %q to %s: %w", seen[id], itemID, err)
		}
		out = append(out, seen[id])
	}
	return out, nil
}

// resolveTagTx maps a normalised name through the alias table to a tag id,
// creating the tag when neither an alias nor a tag matches.
func resolveTagTx(ctx context.Context, tx *sql.Tx, name string) (int64, string, error) {
	current := name
	visited := map[string]bool{}
	for depth := 0; depth < maxAliasDepth; depth++ {
		if visited[current] {
			return 0, "", fmt.Errorf("tag %q: alias cycle at %q", name, current)
		}
		visited[current] = true

		var tagID int64
		err := tx.QueryRowContext(ctx, `SELECT tag_id FROM tag_aliases WHERE alias = ?`, current).Scan(&tagID)
		switch {
		case err == nil:
			var canonical string
			if err := tx.QueryRowContext(ctx, `SELECT name FROM tags WHERE id = ?`, tagID).Scan(&canonical); err != nil {
				return 0, "", fmt.Errorf("tag alias %q points at a missing tag %d: %w", current, tagID, err)
			}
			if canonical == current {
				return tagID, canonical, nil
			}
			current = canonical
			continue
		case !errors.Is(err, sql.ErrNoRows):
			return 0, "", fmt.Errorf("looking up tag alias %q: %w", current, err)
		}

		err = tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = ?`, current).Scan(&tagID)
		switch {
		case err == nil:
			return tagID, current, nil
		case !errors.Is(err, sql.ErrNoRows):
			return 0, "", fmt.Errorf("looking up tag %q: %w", current, err)
		}

		result, err := tx.ExecContext(ctx, `INSERT INTO tags (name) VALUES (?)`, current)
		if err != nil {
			return 0, "", fmt.Errorf("creating tag %q: %w", current, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return 0, "", fmt.Errorf("creating tag %q: %w", current, err)
		}
		return id, current, nil
	}
	return 0, "", fmt.Errorf("tag %q: the alias chain is longer than %d", name, maxAliasDepth)
}

// TagVocabulary is the most-used canonical tags, which the enrichment prompt
// carries so the model reuses a spelling instead of inventing a near-duplicate
// (plans/03 "Prompt files"). Aliases are not included: the model should see
// canon, not history.
func (s *Store) TagVocabulary(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.name FROM tags t
		JOIN item_tags it ON it.tag_id = t.id
		JOIN items i ON i.id = it.item_id AND i.deleted_at IS NULL
		GROUP BY t.id
		ORDER BY count(*) DESC, t.name ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("tag vocabulary: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
