// The enrichment queue's read side. In phase 2 the stage has no model work to
// do: it picks queued items in priority order and marks them ready, so the
// column, the index and the loop shape are real when phase 3 fills them in
// (design.md §5 "Job lifecycle").
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
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

// Enrichment is everything one successful pass writes. It is one struct so the
// item row, its AI tags and the FTS row change in one transaction: a half-written
// enrichment is a row that says it was read when it was not.
type Enrichment struct {
	Summary     string
	ContentText string
	ContentMD   string
	DetailsJSON string
	OGJSON      string

	// Title is the model's cleaned title. It only replaces a placeholder, so a
	// title the owner typed is never overwritten by a later prompt version.
	Title string

	Kind       model.Kind
	KindSource model.KindSource

	PromptVersion  string
	ExtractVersion string

	// Tags are the AI tags. Manual tags survive a re-enrichment.
	Tags []string
}

// ApplyEnrichment writes one successful pass and marks the item ready. A manual
// kind is never overwritten (design.md §6; plans/03 test 10).
func (s *Store) ApplyEnrichment(ctx context.Context, id string, e Enrichment) (model.Item, error) {
	if !e.Kind.Valid() {
		return model.Item{}, fmt.Errorf("store: %q is not a kind", e.Kind)
	}
	if e.KindSource == "" {
		e.KindSource = model.KindSourceAI
	}
	if !e.KindSource.Valid() {
		return model.Item{}, fmt.Errorf("store: %q is not a kind source", e.KindSource)
	}

	now := time.Now().UTC()
	var out model.Item
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		item, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		before := item

		if item.KindSource != model.KindSourceManual {
			item.Kind = e.Kind
			item.KindSource = e.KindSource
		}
		if e.Title != "" && (item.Title == "" || (item.URL != nil && item.Title == *item.URL)) {
			item.Title = e.Title
		}
		item.Summary = e.Summary
		item.ContentText = e.ContentText
		item.ContentMD = e.ContentMD
		item.DetailsJSON = jsonOr(e.DetailsJSON, "{}")
		item.OGJSON = jsonOr(e.OGJSON, "{}")
		item.PromptVersion = e.PromptVersion
		item.ExtractVersion = e.ExtractVersion
		item.ExtractError = ""
		item.State = model.StateReady
		item.Attempts = 0
		item.NextAttemptAt = nil
		item.LastError = ""
		item.EnrichedAt = &now

		if _, err := replaceTagsTx(ctx, tx, id, e.Tags, model.TagSourceAI); err != nil {
			return err
		}
		if err := updateItemRow(ctx, tx, item); err != nil {
			return err
		}
		if err := refreshTagsText(ctx, tx, item.Seq); err != nil {
			return err
		}
		fresh, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if before.DeletedAt == nil {
			if err := ftsDelete(ctx, tx, before); err != nil {
				return err
			}
			if err := ftsInsert(ctx, tx, fresh); err != nil {
				return err
			}
		}
		out = fresh
		return nil
	})
	if err != nil {
		return model.Item{}, err
	}
	return out, nil
}

// SaveEmbedding stores one vector, replacing any previous one for the item.
// dims is the vector's own length, never a constant: the column exists so a
// model change is visible instead of silently comparing 1024 with 768.
func (s *Store) SaveEmbedding(ctx context.Context, itemID, modelName string, vec []float32, contentHash string) error {
	if modelName == "" {
		return errors.New("store: an embedding needs a model name")
	}
	if len(vec) == 0 {
		return errors.New("store: an embedding needs at least one dimension")
	}
	if contentHash == "" {
		return errors.New("store: an embedding needs a content hash")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO item_embeddings
		(item_id, model, dims, content_hash, vec, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET
			model = excluded.model, dims = excluded.dims,
			content_hash = excluded.content_hash, vec = excluded.vec,
			created_at = excluded.created_at`,
		itemID, modelName, len(vec), contentHash, encodeVector(vec), stamp(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("saving embedding for %s: %w", itemID, err)
	}
	return nil
}

// GetEmbedding reads one vector back. Phase 10's search uses it; phase 3's
// tests use it to prove the round trip and the stored dimension.
func (s *Store) GetEmbedding(ctx context.Context, itemID string) (modelName string, vec []float32, contentHash string, err error) {
	var (
		dims int
		blob []byte
	)
	err = s.db.QueryRowContext(ctx,
		`SELECT model, dims, content_hash, vec FROM item_embeddings WHERE item_id = ?`, itemID).
		Scan(&modelName, &dims, &contentHash, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, "", ErrNotFound
	}
	if err != nil {
		return "", nil, "", fmt.Errorf("reading embedding for %s: %w", itemID, err)
	}
	if dims*4 != len(blob) {
		return "", nil, "", fmt.Errorf("store: embedding for %s says %d dims but holds %d bytes", itemID, dims, len(blob))
	}
	vec = make([]float32, dims)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4:]))
	}
	return modelName, vec, contentHash, nil
}

func encodeVector(vec []float32) []byte {
	blob := make([]byte, 4*len(vec))
	for i, value := range vec {
		binary.LittleEndian.PutUint32(blob[i*4:], math.Float32bits(value))
	}
	return blob
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
