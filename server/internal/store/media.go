// Media rows: the attachments of an item. The blob itself lives in the
// content-addressed store (server/internal/media), so a row is a reference — one
// row per attachment, one file per SHA-256, and two rows may point at one file
// when the same photo arrived twice (design.md §11, plans/06-media).
//
// The mirror is why r2_key is here at all: an empty key means "not mirrored
// yet", and the loop drains those rows. A row is never deleted because a mirror
// is pending, and a mirror failure never touches the item's state.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/media"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/google/uuid"
)

// AddMedia records one attachment.
//
// It is idempotent per (item, blob): attaching the same bytes to the same item
// twice returns the existing row untouched rather than a duplicate, which is
// what makes "the same photo saved twice is one attachment" true without the
// caller having to look first. Two different items sharing a blob are two rows
// on purpose — each has its own filename and its own place in the gallery.
func (s *Store) AddMedia(ctx context.Context, m model.Media) (model.Media, error) {
	if m.ItemID == "" {
		return model.Media{}, errors.New("store: media needs an item")
	}
	if !m.Kind.Valid() {
		return model.Media{}, fmt.Errorf("store: %q is not a media kind", m.Kind)
	}
	// The sha256 is a content address: it becomes a path and an R2 key, so a
	// row cannot carry something that is not one.
	if !media.IsHash(m.SHA256) {
		return model.Media{}, fmt.Errorf("store: %q is not a sha256", m.SHA256)
	}
	if m.LocalPath == "" {
		return model.Media{}, errors.New("store: media needs a local_path")
	}

	var out model.Media
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		existing, err := mediaByItemAndHash(ctx, tx, m.ItemID, m.SHA256)
		if err == nil {
			out = existing
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if m.ID == "" {
			m.ID = uuid.NewString()
		}
		if m.CreatedAt.IsZero() {
			m.CreatedAt = time.Now().UTC()
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO media (id, item_id, kind, filename, content_type, bytes, sha256,
			                   width, height, local_path, r2_key, pdf_text_state, derived_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.ItemID, string(m.Kind), m.Filename, m.ContentType, m.Bytes, m.SHA256,
			m.Width, m.Height, m.LocalPath, m.R2Key, m.PDFTextState,
			m.DerivedAt, m.CreatedAt.UTC())
		if err != nil {
			return fmt.Errorf("store: adding media: %w", err)
		}
		fresh, err := mediaByID(ctx, tx, m.ID)
		if err != nil {
			return err
		}
		out = fresh
		return nil
	})
	return out, err
}

// MediaByItem lists an item's attachments in a stable order: the order they were
// added, which is the order the gallery and a Telegram album were sent in.
func (s *Store) MediaByItem(ctx context.Context, itemID string) ([]model.Media, error) {
	rows, err := s.db.QueryContext(ctx, mediaColumns+`
		WHERE item_id = ? ORDER BY created_at, id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("store: reading media: %w", err)
	}
	defer rows.Close()
	return scanMedia(rows)
}

// MediaBySHA returns every row that shares one blob, so a caller can count
// references before unlinking a file or deleting an R2 key (plans/06 test 13).
func (s *Store) MediaBySHA(ctx context.Context, sha string) ([]model.Media, error) {
	if !media.IsHash(sha) {
		return nil, fmt.Errorf("store: %q is not a sha256", sha)
	}
	rows, err := s.db.QueryContext(ctx, mediaColumns+`
		WHERE sha256 = ? ORDER BY created_at, id`, sha)
	if err != nil {
		return nil, fmt.Errorf("store: reading media: %w", err)
	}
	defer rows.Close()
	return scanMedia(rows)
}

// MediaNeedingMirror is the mirror loop's queue: rows with no R2 key yet, oldest
// first, so a backlog drains in the order it arrived.
func (s *Store) MediaNeedingMirror(ctx context.Context, limit int) ([]model.Media, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, mediaColumns+`
		WHERE r2_key = '' ORDER BY created_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: reading media: %w", err)
	}
	defer rows.Close()
	return scanMedia(rows)
}

// SetMediaMirror records the R2 key a blob was uploaded to. An empty key is
// refused: that is the state it is already in, and accepting it would make a
// failed upload look like a successful one and the row would be retried forever
// without anyone noticing.
func (s *Store) SetMediaMirror(ctx context.Context, id, r2Key string) error {
	if r2Key == "" {
		return errors.New("store: an empty r2 key is not a mirror")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE media SET r2_key = ? WHERE id = ?`, r2Key, id)
	if err != nil {
		return fmt.Errorf("store: recording the mirror: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: recording the mirror: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: no media row %q", id)
	}
	return nil
}

// SetMediaDimensions records what an image turned out to be, which the thumbnail
// already learned and the row would otherwise never know.
func (s *Store) SetMediaDimensions(ctx context.Context, id string, width, height int) error {
	if width < 0 || height < 0 {
		return fmt.Errorf("store: %dx%d is not a size", width, height)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE media SET width = ?, height = ? WHERE id = ?`,
		width, height, id)
	if err != nil {
		return fmt.Errorf("store: recording dimensions: %w", err)
	}
	// A row that is not there is an error, not a no-op: a purge can take an item
	// while the pipeline is holding it, and losing a write silently is how that
	// becomes an invisible bug (found in review).
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: recording dimensions: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: no media row %q", id)
	}
	return nil
}

const mediaColumns = `SELECT id, item_id, kind, filename, content_type, bytes, sha256,
	width, height, local_path, r2_key, pdf_text_state, derived_at, created_at FROM media`

func mediaByID(ctx context.Context, tx *sql.Tx, id string) (model.Media, error) {
	row := tx.QueryRowContext(ctx, mediaColumns+` WHERE id = ?`, id)
	var (
		m       model.Media
		kind    string
		width   sql.NullInt64
		height  sql.NullInt64
		derived sql.NullString
		created string
	)
	if err := row.Scan(&m.ID, &m.ItemID, &kind, &m.Filename, &m.ContentType, &m.Bytes, &m.SHA256,
		&width, &height, &m.LocalPath, &m.R2Key, &m.PDFTextState, &derived, &created); err != nil {
		return model.Media{}, err
	}
	return finishMedia(m, kind, width, height, derived, created)
}

func mediaByItemAndHash(ctx context.Context, tx *sql.Tx, itemID, sha string) (model.Media, error) {
	row := tx.QueryRowContext(ctx, mediaColumns+` WHERE item_id = ? AND sha256 = ?`, itemID, sha)
	var (
		m       model.Media
		kind    string
		width   sql.NullInt64
		height  sql.NullInt64
		derived sql.NullString
		created string
	)
	if err := row.Scan(&m.ID, &m.ItemID, &kind, &m.Filename, &m.ContentType, &m.Bytes, &m.SHA256,
		&width, &height, &m.LocalPath, &m.R2Key, &m.PDFTextState, &derived, &created); err != nil {
		return model.Media{}, err
	}
	return finishMedia(m, kind, width, height, derived, created)
}

func scanMedia(rows *sql.Rows) ([]model.Media, error) {
	out := []model.Media{}
	for rows.Next() {
		var (
			m       model.Media
			kind    string
			width   sql.NullInt64
			height  sql.NullInt64
			derived sql.NullString
			created string
		)
		if err := rows.Scan(&m.ID, &m.ItemID, &kind, &m.Filename, &m.ContentType, &m.Bytes, &m.SHA256,
			&width, &height, &m.LocalPath, &m.R2Key, &m.PDFTextState, &derived, &created); err != nil {
			return nil, err
		}
		media, err := finishMedia(m, kind, width, height, derived, created)
		if err != nil {
			return nil, err
		}
		out = append(out, media)
	}
	return out, rows.Err()
}

// finishMedia turns the nullable columns into the model's pointers and parses
// the timestamp, which SQLite hands back as text.
func finishMedia(m model.Media, kind string, width, height sql.NullInt64, derived sql.NullString, created string) (model.Media, error) {
	m.Kind = model.MediaKind(kind)
	if width.Valid {
		w := int(width.Int64)
		m.Width = &w
	}
	if height.Valid {
		h := int(height.Int64)
		m.Height = &h
	}
	if derived.Valid && derived.String != "" {
		if at, err := parseStamp(derived.String); err == nil {
			m.DerivedAt = &at
		}
	}
	at, err := parseStamp(created)
	if err != nil {
		return model.Media{}, fmt.Errorf("media %s created_at: %w", m.ID, err)
	}
	m.CreatedAt = at
	return m, nil
}
