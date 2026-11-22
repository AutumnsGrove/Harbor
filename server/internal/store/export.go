package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// ExportLibrary reads the whole library in one transaction (plans/12a).
//
// Every item is included, soft-deleted ones too: "Recently deleted" is still the
// owner's library, and an export that quietly drops it is not the copy that
// survives Harbor. Ordering is by stable keys (seq, item id, tag name, sha256) so
// two exports of an unchanged library are byte-identical and the manifest can be
// diffed.
func (s *Store) ExportLibrary(ctx context.Context) (model.Library, error) {
	library := model.Library{Tags: map[string][]string{}, SpaceItems: map[string][]string{}}
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		items, err := exportItems(ctx, tx)
		if err != nil {
			return err
		}
		library.Items = items
		if library.Tags, err = exportTags(ctx, tx); err != nil {
			return err
		}
		if library.Spaces, err = exportSpaces(ctx, tx); err != nil {
			return err
		}
		if library.SpaceItems, err = exportSpaceItems(ctx, tx); err != nil {
			return err
		}
		if library.Highlights, err = exportHighlights(ctx, tx); err != nil {
			return err
		}
		library.Media, err = exportMedia(ctx, tx)
		return err
	})
	if err != nil {
		return model.Library{}, fmt.Errorf("store: exporting the library: %w", err)
	}
	return library, nil
}

// SchemaVersion is the highest applied migration. The export records it so a
// restore can refuse a file it is too old to read.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("store: reading the schema version: %w", err)
	}
	return int(version.Int64), nil
}

func exportItems(ctx context.Context, tx *sql.Tx) ([]model.Item, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+itemColumns+` FROM items ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []model.Item
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func exportTags(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT it.item_id, t.name
		FROM item_tags it JOIN tags t ON t.id = it.tag_id
		ORDER BY it.item_id, t.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tags := map[string][]string{}
	for rows.Next() {
		var itemID, name string
		if err := rows.Scan(&itemID, &name); err != nil {
			return nil, err
		}
		tags[itemID] = append(tags[itemID], name)
	}
	return tags, rows.Err()
}

func exportSpaces(ctx context.Context, tx *sql.Tx) ([]model.Space, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, slug, name, kind, rule_json, created_at FROM spaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var spaces []model.Space
	for rows.Next() {
		var (
			space   model.Space
			slug    sql.NullString
			kind    string
			created string
		)
		if err := rows.Scan(&space.ID, &slug, &space.Name, &kind, &space.RuleJSON, &created); err != nil {
			return nil, err
		}
		if slug.Valid {
			value := slug.String
			space.Slug = &value
		}
		space.Kind = model.SpaceKind(kind)
		if space.CreatedAt, err = parseStamp(created); err != nil {
			return nil, err
		}
		spaces = append(spaces, space)
	}
	return spaces, rows.Err()
}

func exportSpaceItems(ctx context.Context, tx *sql.Tx) (map[string][]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT space_id, item_id FROM space_items ORDER BY space_id, item_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	members := map[string][]string{}
	for rows.Next() {
		var spaceID, itemID string
		if err := rows.Scan(&spaceID, &itemID); err != nil {
			return nil, err
		}
		members[spaceID] = append(members[spaceID], itemID)
	}
	return members, rows.Err()
}

func exportHighlights(ctx context.Context, tx *sql.Tx) ([]model.Highlight, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, item_id, quote, note, color, anchor_json, detached, created_at
		FROM highlights ORDER BY item_id, created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var highlights []model.Highlight
	for rows.Next() {
		var (
			highlight model.Highlight
			color     string
			created   string
			detached  int
		)
		if err := rows.Scan(&highlight.ID, &highlight.ItemID, &highlight.Quote, &highlight.Note,
			&color, &highlight.AnchorJSON, &detached, &created); err != nil {
			return nil, err
		}
		highlight.Color = model.HighlightColor(color)
		highlight.Detached = detached != 0
		if highlight.CreatedAt, err = parseStamp(created); err != nil {
			return nil, err
		}
		highlights = append(highlights, highlight)
	}
	return highlights, rows.Err()
}

func exportMedia(ctx context.Context, tx *sql.Tx) ([]model.Media, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, item_id, kind, filename, content_type, bytes, sha256, width, height,
		       local_path, r2_key, pdf_text_state, derived_at, created_at
		FROM media ORDER BY item_id, sha256, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var media []model.Media
	for rows.Next() {
		var (
			entry   model.Media
			kind    string
			width   sql.NullInt64
			height  sql.NullInt64
			derived sql.NullString
			created string
		)
		if err := rows.Scan(&entry.ID, &entry.ItemID, &kind, &entry.Filename, &entry.ContentType,
			&entry.Bytes, &entry.SHA256, &width, &height, &entry.LocalPath, &entry.R2Key,
			&entry.PDFTextState, &derived, &created); err != nil {
			return nil, err
		}
		entry.Kind = model.MediaKind(kind)
		if width.Valid {
			value := int(width.Int64)
			entry.Width = &value
		}
		if height.Valid {
			value := int(height.Int64)
			entry.Height = &value
		}
		if entry.DerivedAt, err = nullableTime(derived); err != nil {
			return nil, err
		}
		if entry.CreatedAt, err = parseStamp(created); err != nil {
			return nil, err
		}
		media = append(media, entry)
	}
	return media, rows.Err()
}
