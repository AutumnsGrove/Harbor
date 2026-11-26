// The enrichment queue's read side. In phase 2 the stage has no model work to
// do: it picks queued items in priority order and marks them ready, so the
// column, the index and the loop shape are real when phase 3 fills them in
// (design.md §5 "Job lifecycle").
package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// QueuedItems is one enrichment batch: due work only, the highest priority
// first, then the earliest due, then oldest saved so a backlog drains in order.
// A NULL next_attempt_at is due now; a failed item waiting out its backoff is
// not returned until its turn.
func (s *Store) QueuedItems(ctx context.Context, limit int) ([]model.Item, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+itemColumns+` FROM items
		WHERE state = 'queued' AND deleted_at IS NULL
		  AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY priority DESC, next_attempt_at IS NULL DESC, next_attempt_at ASC, saved_at ASC, seq ASC
		LIMIT ?`, Stamp(time.Now().UTC()), limit)
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

	// Embedding is written in the same transaction as the row when set. A pass
	// that has no vector (an embedding failure) still enriches; the retry task
	// fills the vector later.
	Embedding      []float32
	EmbeddingModel string
	EmbeddingHash  string
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
		if len(e.Embedding) > 0 {
			if err := saveEmbeddingTx(ctx, tx, id, e.EmbeddingModel, e.Embedding, e.EmbeddingHash); err != nil {
				return err
			}
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
	return s.withTx(ctx, func(tx *sql.Tx) error {
		return saveEmbeddingTx(ctx, tx, itemID, modelName, vec, contentHash)
	})
}

func saveEmbeddingTx(ctx context.Context, tx *sql.Tx, itemID, modelName string, vec []float32, contentHash string) error {
	if modelName == "" {
		return errors.New("store: an embedding needs a model name")
	}
	if len(vec) == 0 {
		return errors.New("store: an embedding needs at least one dimension")
	}
	if contentHash == "" {
		return errors.New("store: an embedding needs a content hash")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO item_embeddings
		(item_id, model, dims, content_hash, vec, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET
			model = excluded.model, dims = excluded.dims,
			content_hash = excluded.content_hash, vec = excluded.vec,
			created_at = excluded.created_at`,
		itemID, modelName, len(vec), contentHash, encodeVector(vec), Stamp(time.Now().UTC()))
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

// RecordEnrichmentFailure advances the retry columns after a failed pass. A
// next attempt of nil parks the item as failed; otherwise it stays queued and
// is invisible to QueuedItems until the delay elapses (design.md §6).
func (s *Store) RecordEnrichmentFailure(ctx context.Context, id, message string, next *time.Time) (model.Item, error) {
	state := model.StateQueued
	if next == nil {
		state = model.StateFailed
	}
	if len(message) > 1_000 {
		message = message[:1_000]
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE items
		SET attempts = attempts + 1,
		    next_attempt_at = ?,
		    last_error = ?,
		    state = ?
		WHERE id = ?`,
		nullableStamp(next), message, string(state), id); err != nil {
		return model.Item{}, fmt.Errorf("recording failure for %s: %w", id, err)
	}
	return s.GetItem(ctx, id)
}

// MarkReady closes the enrichment stage for an item. It is idempotent: marking
// a ready item ready again is a no-op rather than an error.
func (s *Store) MarkReady(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE items
		SET state = 'ready', enriched_at = ?, next_attempt_at = NULL, last_error = ''
		WHERE id = ? AND state = 'queued'`, Stamp(time.Now().UTC()), id); err != nil {
		return fmt.Errorf("marking %s ready: %w", id, err)
	}
	// Zero rows means it was already ready, already failed, or gone; none of
	// those is an error to the caller.
	return nil
}

// QueueUnenriched puts every item that has never been enriched back in the
// queue, so a bulk import that landed under `--no-enrich` can be enriched later
// as the separate, watched run the plan describes.
//
// It is deliberately narrow: state must be `ready` (so nothing already queued,
// enriching or failed is disturbed), prompt_version must be empty (only
// ApplyEnrichment writes one, and the pipeline refuses to run without a prompt
// version, so an item the model has read is never re-read — a prompt upgrade is a
// different, deliberate job), and a soft-deleted row stays
// in the bin. `enriched_at` is not the marker: `MarkReady` stamps it, because
// that column records when the stage closed, which `--no-enrich` closes without
// the model seeing anything.
//
// priority is the caller's: the import backlog runs at -1 so a link saved by
// hand is still enriched first (design.md §5). limit <= 0 means all of them.
func (s *Store) QueueUnenriched(ctx context.Context, priority, limit int) (queued, remaining int, err error) {
	if limit <= 0 {
		limit = -1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE items SET
		state = 'queued', priority = ?, attempts = 0, next_attempt_at = NULL, last_error = ''
		WHERE seq IN (
			SELECT seq FROM items
			WHERE state = 'ready' AND prompt_version = '' AND deleted_at IS NULL
			ORDER BY saved_at ASC, seq ASC
			LIMIT ?
		)`, priority, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("queueing unenriched items: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("queueing unenriched items: %w", err)
	}
	remaining, err = s.UnenrichedCount(ctx)
	if err != nil {
		return int(affected), 0, err
	}
	return int(affected), remaining, nil
}

// UnenrichedCount is how many items are still waiting for their first pass, in
// any state other than the queue — which is what a bulk import leaves behind and
// what the operator wants before (and after) spending on a drain.
func (s *Store) UnenrichedCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM items
		WHERE state = 'ready' AND prompt_version = '' AND deleted_at IS NULL`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting unenriched items: %w", err)
	}
	return n, nil
}

// VideoMetadata is the pass that files a video whose page could not be read.
//
// It exists because "the page is walled" is not the same as "there is nothing to
// store": a video's public metadata still gives it a title, a channel and a
// thumbnail, and an item with those is usable and findable even before phase 7
// fetches its transcript.
type VideoMetadata struct {
	// Title is the video's own title. It only replaces a placeholder, so a title
	// the owner typed is never overwritten (same rule as ApplyEnrichment).
	Title string
	// Channel is the uploader's name, stored as the video's author.
	Channel string
	// Thumbnail is the still image; it is the item's cover until phase 6.
	Thumbnail string
	// PageURL is the canonical watch page, kept in details_json so the kind
	// panel can link back to it.
	PageURL string
	// Reason says why the page itself was not read. It is kept on the row so the
	// item explains itself later instead of looking unfinished by accident.
	Reason string
	// PromptVersion is what wrote this row. It is not a prompt — no model ran —
	// which is exactly why it has its own value: a later pass can find these
	// rows by it (phase 7 re-enriches every `oembed.v1` video), and the
	// enrichment loop will not mistake them for work a prompt has done.
	PromptVersion string
}

// ApplyVideoMetadata records that pass in one transaction: the title if it was a
// placeholder, the video kind, the cover and channel in details_json, the reason
// in extract_error, and the row ready.
//
// It touches no tags and no summary: no model saw this item, so inventing either
// would be a lie the UI cannot take back. An item whose kind is manual keeps it.
func (s *Store) ApplyVideoMetadata(ctx context.Context, id string, m VideoMetadata) (model.Item, error) {
	if strings.TrimSpace(m.Title) == "" {
		return model.Item{}, fmt.Errorf("store: video metadata for %s has no title", id)
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
			// The host decided this, not a model: the channel is a video page.
			item.Kind = model.KindVideo
			item.KindSource = model.KindSourceDefault
		}
		if item.Title == "" || (item.URL != nil && item.Title == *item.URL) {
			item.Title = strings.TrimSpace(m.Title)
		}
		item.DetailsJSON = videoDetails(item.DetailsJSON, m)
		item.PromptVersion = m.PromptVersion
		item.ExtractError = m.Reason
		item.State = model.StateReady
		item.Attempts = 0
		item.NextAttemptAt = nil
		item.LastError = ""
		item.EnrichedAt = &now

		if err := updateItemRow(ctx, tx, item); err != nil {
			return err
		}
		fresh, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		// The title is the only indexed value that changed, and FTS5 cannot
		// update in place.
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
	return out, err
}

// videoDetails merges the video's facts into details_json without dropping what
// a JSON-LD pass already stored (the import wrote none, but a re-save might).
func videoDetails(existing string, m VideoMetadata) string {
	details := map[string]any{}
	if strings.TrimSpace(existing) != "" && existing != "{}" {
		if err := json.Unmarshal([]byte(existing), &details); err != nil {
			details = map[string]any{}
		}
	}
	details["type"] = "VideoObject"
	details["name"] = strings.TrimSpace(m.Title)
	if channel := strings.TrimSpace(m.Channel); channel != "" {
		details["author"] = []string{channel}
	}
	if thumb := strings.TrimSpace(m.Thumbnail); thumb != "" {
		details["image"] = thumb
		details["thumbnailUrl"] = thumb
	}
	if page := strings.TrimSpace(m.PageURL); page != "" {
		details["url"] = page
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return jsonOr(existing, "{}")
	}
	return string(encoded)
}

// relativeCoverWhere is the predicate for "this row's stored page image is not
// something fetch can use": present, but not an absolute http(s) URL. Both keys
// count, because the importer writes `cover` and enrichment writes `image`
// (issue #49 measured them as disjoint).
//
// The CASE keeps json_extract away from a row whose og_json is not JSON at all;
// SQLite does not promise to short-circuit AND.
const relativeCoverWhere = `CASE WHEN json_valid(og_json) THEN (
	(coalesce(json_extract(og_json, '$.image'), '') <> ''
		AND json_extract(og_json, '$.image') NOT LIKE 'http://%'
		AND json_extract(og_json, '$.image') NOT LIKE 'https://%')
	OR (coalesce(json_extract(og_json, '$.cover'), '') <> ''
		AND json_extract(og_json, '$.cover') NOT LIKE 'http://%'
		AND json_extract(og_json, '$.cover') NOT LIKE 'https://%')
) ELSE 0 END`

// RelativeCoverCount is how many finished rows still hold a page image the
// proxy cannot fetch: a root-relative or protocol-relative og:image stored
// before the pipeline resolved it (issue #67).
func (s *Store) RelativeCoverCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM items
		WHERE state = 'ready' AND deleted_at IS NULL AND `+relativeCoverWhere).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting items with a relative cover: %w", err)
	}
	return n, nil
}

// QueueRelativeCovers re-queues finished rows whose stored image is relative, so
// a re-enrichment can overwrite it with the resolved URL. It is separate from
// QueueUnenriched on purpose: that one only ever touches rows the model has
// never read, and these rows have been read — this is the one deliberate reason
// to read them again.
//
// limit <= 0 means all of them, and the caller owns the priority so a hand-saved
// link still goes first.
func (s *Store) QueueRelativeCovers(ctx context.Context, priority, limit int) (queued, remaining int, err error) {
	if limit <= 0 {
		limit = -1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE items SET
		state = 'queued', priority = ?, attempts = 0, next_attempt_at = NULL, last_error = ''
		WHERE seq IN (
			SELECT seq FROM items
			WHERE state = 'ready' AND deleted_at IS NULL AND `+relativeCoverWhere+`
			ORDER BY saved_at ASC, seq ASC
			LIMIT ?
		)`, priority, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("queueing items with a relative cover: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("queueing items with a relative cover: %w", err)
	}
	remaining, err = s.RelativeCoverCount(ctx)
	if err != nil {
		return int(affected), 0, err
	}
	return int(affected), remaining, nil
}

// Unreadable is a row filed without its page, because the page said no. It is
// not a failure and not a partial success: there is a save, and there is a
// reason the library cannot show what is inside it (issue #76, design §6).
type Unreadable struct {
	// Title is the page's own title when the answer carried one. It only
	// replaces a placeholder, so a title the owner typed is never overwritten.
	Title string
	// Reason says what this row does not have. It lands in extract_error, where
	// the card and the item page already look for it.
	Reason string
	// Marker is what wrote the row, in prompt_version: "wall.v1" or "dead.v1".
	// It is not a prompt — no model ran — which is exactly why it has its own
	// value: the UI badges the row by it, and a manual retry clears it. Same
	// idiom as VideoMetadataVersion.
	Marker string
	// Dead marks the link itself as gone (link_status='dead'), which is phase
	// 11's column. The checker later updates that verdict rather than fighting
	// it.
	Dead bool
}

// ApplyUnreadable files a row the page refused: ready, with its own title, the
// reason kept, and the marker that says what kind of refusal it was. It writes
// no tags, no summary and no embedding, and makes no model call — a summary
// invented from a title alone is a fabrication the library cannot take back.
func (s *Store) ApplyUnreadable(ctx context.Context, id string, u Unreadable) (model.Item, error) {
	if strings.TrimSpace(u.Marker) == "" {
		return model.Item{}, fmt.Errorf("store: an unreadable item needs a marker")
	}
	if strings.TrimSpace(u.Reason) == "" {
		return model.Item{}, fmt.Errorf("store: an unreadable item needs a reason")
	}
	now := time.Now().UTC()

	var out model.Item
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		item, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		before := item

		if title := strings.TrimSpace(u.Title); title != "" {
			if item.Title == "" || (item.URL != nil && item.Title == *item.URL) {
				item.Title = title
			}
		}
		item.PromptVersion = u.Marker
		item.ExtractError = u.Reason
		item.State = model.StateReady
		item.Attempts = 0
		item.NextAttemptAt = nil
		item.LastError = ""
		item.EnrichedAt = &now
		if u.Dead {
			item.LinkStatus = LinkDead
			item.LastCheckedAt = &now
		}

		if err := updateItemRow(ctx, tx, item); err != nil {
			return err
		}
		fresh, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		// The title is the only indexed value that changed, and FTS5 cannot
		// update in place.
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

// LinkStatus values are design §7's: what a link checker can conclude about a
// URL, and nothing about the page's contents.
const (
	LinkOK    = model.LinkOK
	LinkDead  = model.LinkDead
	LinkError = model.LinkError
)

// FailedCount is how many parked rows an older pipeline left in Needs attention.
// It is the number `harbor enrich --refile-failed` reports before anything is
// touched.
func (s *Store) FailedCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM items WHERE state = 'failed' AND deleted_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting failed items: %w", err)
	}
	return n, nil
}

// QueueFailed puts the parked rows back in the queue so the current filing rules
// get to see them. It exists because a rule that improves what happens to a
// refused or dead page cannot help the rows an older rule already parked: the
// 403s and 404s of issue #76 sit in `failed` until something re-reads them.
//
// They come back at the interactive priority, not the import backlog's: these
// are her saves, and the run is watched. `last_error` is cleared so a row that
// files cleanly leaves no stale reason behind; a page that fails again writes
// its own.
func (s *Store) QueueFailed(ctx context.Context, priority, limit int) (queued, remaining int, err error) {
	if limit <= 0 {
		limit = -1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE items SET
		state = 'queued', priority = ?, attempts = 0, next_attempt_at = NULL, last_error = ''
		WHERE seq IN (
			SELECT seq FROM items
			WHERE state = 'failed' AND deleted_at IS NULL
			ORDER BY saved_at ASC, seq ASC
			LIMIT ?
		)`, priority, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("requeueing failed items: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("requeueing failed items: %w", err)
	}
	remaining, err = s.FailedCount(ctx)
	if err != nil {
		return int(affected), 0, err
	}
	return int(affected), remaining, nil
}
