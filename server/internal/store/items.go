// Item rows: the save path (design.md §5), the item/FTS pair (design.md §7),
// the three clocks, and the soft-delete lifecycle. This is the only place that
// writes an item, so it is the only place that has to get the FTS external
// content recipe right.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// ErrNotFound is returned when an id does not name a row. Callers turn it into
// a 404; the store never invents an empty item.
var ErrNotFound = errors.New("store: not found")

// ErrDuplicateURL is returned by CreateItem when the canonical URL already
// belongs to an item, including a soft-deleted one (design.md §7). The save
// path calls MergeItem and never sees this.
var ErrDuplicateURL = errors.New("store: an item with that canonical URL already exists")

// ItemInput is one save, whatever door it came through. URL is the *canonical*
// URL (canon.CanonicalURL has already run); nil means a pure note.
type ItemInput struct {
	ID          string
	URL         *string
	URLOriginal string
	Title       string
	Note        string

	Kind       model.Kind       // empty: note when URL is nil, article otherwise
	KindSource model.KindSource // empty: default
	Source     model.Source     // empty: app

	Summary     string
	ContentText string
	ContentMD   string
	DetailsJSON string
	OGJSON      string

	ImportRef string
	Priority  int
	Tags      []string
	ReadLater bool

	// Imports preserve history: CreatedAt and SavedAt are honoured when set.
	// A normal save leaves them zero and gets the same "now".
	CreatedAt time.Time
	SavedAt   time.Time
}

// ItemPatch is an edit. A nil field is left alone; that is the difference
// between "the user changed this" and "the user did not touch it".
type ItemPatch struct {
	Title   *string
	Note    *string
	Summary *string
	Kind    *model.Kind
}

// ListOptions is one page of the home grid: newest saved first, soft-deleted
// rows hidden unless asked for.
type ListOptions struct {
	Limit          int
	After          *Cursor
	IncludeDeleted bool
}

// Cursor is the (saved_at, seq) pair the list pages on. seq breaks the tie when
// two items share a second, which an import will routinely produce.
type Cursor struct {
	SavedAt time.Time
	Seq     int64
}

const (
	defaultListLimit = 50
	maxListLimit     = 500
)

// itemColumns is the read order scanItem expects, in design.md §7 order.
const itemColumns = `seq, id, url, url_original, title, note, kind, kind_source, summary,
	content_text, content_md, details_json, og_json, tags_text, source, import_ref,
	state, priority, attempts, next_attempt_at, last_error, prompt_version,
	extract_version, extract_error, enriched_at, transcript_text, transcript_state,
	transcript_lang, transcript_fetched_at, link_status, last_checked_at, deleted_at,
	created_at, saved_at, updated_at`

// CreateItem writes a new item and its FTS row in one transaction. A duplicate
// canonical URL is ErrDuplicateURL — the merge rule lives in MergeItem, so a
// caller that wants merge-on-resave has to say so.
func (s *Store) CreateItem(ctx context.Context, in ItemInput) (model.Item, error) {
	item, err := s.writeItem(ctx, in, false)
	if err != nil {
		return model.Item{}, err
	}
	return item, nil
}

// MergeItem is the save path from design.md §5: saving a canonical URL that is
// already in the library merges into that item instead of creating a second
// one. merged reports whether an existing row was used, which the composer's
// warning and the queue's logging both want.
func (s *Store) MergeItem(ctx context.Context, in ItemInput) (item model.Item, merged bool, err error) {
	if !hasURL(in) {
		// A note has no key to merge on: two identical notes are two thoughts.
		item, err := s.writeItem(ctx, in, false)
		return item, false, err
	}
	item, err = s.writeItem(ctx, in, true)
	if err != nil {
		return model.Item{}, false, err
	}
	return item, true, nil
}

func hasURL(in ItemInput) bool { return in.URL != nil && strings.TrimSpace(*in.URL) != "" }

// writeItem is the single insert/merge implementation, so the two entry points
// cannot drift.
func (s *Store) writeItem(ctx context.Context, in ItemInput, merge bool) (model.Item, error) {
	if strings.TrimSpace(in.ID) == "" {
		return model.Item{}, errors.New("store: an item needs an id")
	}
	if err := validate(in); err != nil {
		return model.Item{}, err
	}

	var out model.Item
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var (
			existing model.Item
			found    bool
		)
		if merge {
			row, err := scanItemRow(tx.QueryRowContext(ctx,
				`SELECT `+itemColumns+` FROM items WHERE url = ?`, *in.URL))
			switch {
			case err == nil:
				existing, found = row, true
			case errors.Is(err, ErrNotFound):
				existing, found = model.Item{}, false
			default:
				return err
			}
		}

		now := time.Now().UTC()
		created := in.CreatedAt.UTC()
		if created.IsZero() {
			created = now
		}
		saved := in.SavedAt.UTC()
		if saved.IsZero() {
			saved = created
		}

		if !found {
			item := model.Item{
				ID:              in.ID,
				URL:             in.URL,
				URLOriginal:     in.URLOriginal,
				Title:           in.Title,
				Note:            strings.TrimSpace(in.Note),
				Kind:            kindOrDefault(in.Kind, in.URL),
				KindSource:      kindSourceOrDefault(in.KindSource),
				Summary:         in.Summary,
				ContentText:     in.ContentText,
				ContentMD:       in.ContentMD,
				DetailsJSON:     jsonOr(in.DetailsJSON, "{}"),
				OGJSON:          jsonOr(in.OGJSON, "{}"),
				Source:          sourceOrDefault(in.Source),
				ImportRef:       in.ImportRef,
				State:           model.StateQueued,
				Priority:        in.Priority,
				LinkStatus:      "ok",
				CreatedAt:       created,
				SavedAt:         saved,
				UpdatedAt:       created,
				TranscriptState: "",
				LastError:       "",
				ExtractError:    "",
			}
			if err := insertItem(ctx, tx, item); err != nil {
				return err
			}
			seq, err := lastSeq(ctx, tx)
			if err != nil {
				return err
			}
			item.Seq = seq

			if err := syncTags(ctx, tx, item.ID, in.Tags); err != nil {
				return err
			}
			if in.ReadLater {
				if err := addToReadLater(ctx, tx, item.ID); err != nil {
					return err
				}
			}
			if err := refreshTagsText(ctx, tx, item.Seq); err != nil {
				return err
			}
			stored, err := getItemTx(ctx, tx, item.ID)
			if err != nil {
				return err
			}
			if err := ftsInsert(ctx, tx, stored); err != nil {
				return err
			}
			out = stored
			return nil
		}

		// Merge: design.md §5. The note appends, tags union, Read later is
		// sticky, metadata refreshes when this save brought some, saved_at
		// moves and created_at does not. updated_at is for edits (UpdateItem),
		// so it is deliberately untouched here.
		merged := existing
		merged.Note = appendNote(existing.Note, in.Note, saved)
		if in.Title != "" {
			merged.Title = in.Title
		}
		if in.URLOriginal != "" {
			merged.URLOriginal = in.URLOriginal
		}
		if in.Summary != "" {
			merged.Summary = in.Summary
		}
		if in.ContentText != "" {
			merged.ContentText = in.ContentText
		}
		if in.ContentMD != "" {
			merged.ContentMD = in.ContentMD
		}
		if in.DetailsJSON != "" {
			merged.DetailsJSON = in.DetailsJSON
		}
		if in.OGJSON != "" {
			merged.OGJSON = in.OGJSON
		}
		if in.Source != "" {
			merged.Source = in.Source
		}
		if in.ImportRef != "" {
			merged.ImportRef = in.ImportRef
		}
		merged.SavedAt = saved
		// Re-saving a deleted URL restores it (design.md §5); the unique index
		// deliberately counts deleted rows, which is why we get here at all.
		merged.DeletedAt = nil

		// A soft-deleted item was already retracted from FTS; only a live row
		// still has indexed values to delete.
		if existing.DeletedAt == nil {
			if err := ftsDelete(ctx, tx, existing); err != nil {
				return err
			}
		}
		if err := updateItemRow(ctx, tx, merged); err != nil {
			return err
		}
		if err := syncTags(ctx, tx, merged.ID, in.Tags); err != nil {
			return err
		}
		if in.ReadLater {
			if err := addToReadLater(ctx, tx, merged.ID); err != nil {
				return err
			}
		}
		if err := refreshTagsText(ctx, tx, merged.Seq); err != nil {
			return err
		}
		fresh, err := getItemTx(ctx, tx, merged.ID)
		if err != nil {
			return err
		}
		if err := ftsInsert(ctx, tx, fresh); err != nil {
			return err
		}
		out = fresh
		return nil
	})
	if err != nil {
		return model.Item{}, err
	}
	return out, nil
}

// GetItem is by public id and includes soft-deleted rows: the Recently deleted
// view and the merge path both need to see them (design.md §7).
func (s *Store) GetItem(ctx context.Context, id string) (model.Item, error) {
	return scanItemRow(s.db.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM items WHERE id = ?`, id))
}

// ListItems pages the home grid: saved_at newest first, soft-deleted hidden by
// default, one row lookahead not needed because the cursor is total.
func (s *Store) ListItems(ctx context.Context, opts ListOptions) ([]model.Item, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	var (
		where []string
		args  []any
	)
	if !opts.IncludeDeleted {
		where = append(where, "deleted_at IS NULL")
	}
	if opts.After != nil {
		where = append(where, "(saved_at < ? OR (saved_at = ? AND seq < ?))")
		args = append(args, stamp(opts.After.SavedAt), stamp(opts.After.SavedAt), opts.After.Seq)
	}
	query := `SELECT ` + itemColumns + ` FROM items`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY saved_at DESC, seq DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
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

// UpdateItem is an edit: updated_at moves, saved_at does not, and the FTS row is
// rewritten from the old values so an old term stops matching (design.md §7).
func (s *Store) UpdateItem(ctx context.Context, id string, p ItemPatch) (model.Item, error) {
	var out model.Item
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		item, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		before := item // the FTS row still holds these values
		if p.Title != nil {
			item.Title = *p.Title
		}
		if p.Note != nil {
			item.Note = *p.Note
		}
		if p.Summary != nil {
			item.Summary = *p.Summary
		}
		if p.Kind != nil {
			if !p.Kind.Valid() {
				return fmt.Errorf("store: %q is not a kind", *p.Kind)
			}
			item.Kind = *p.Kind
			item.KindSource = model.KindSourceManual // manual is never overwritten
		}
		item.UpdatedAt = time.Now().UTC()

		if err := ftsDelete(ctx, tx, before); err != nil {
			return err
		}
		if err := updateItemRow(ctx, tx, item); err != nil {
			return err
		}
		fresh, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := ftsInsert(ctx, tx, fresh); err != nil {
			return err
		}
		out = fresh
		return nil
	})
	return out, err
}

// SoftDeleteItem marks an item deleted and drops it from FTS, so both the grid
// and search stop seeing it without losing the row (plans/02 test 6).
func (s *Store) SoftDeleteItem(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		item, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if item.DeletedAt != nil {
			return nil // idempotent: deleting twice is not an error
		}
		if err := ftsDelete(ctx, tx, item); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET deleted_at = ? WHERE id = ?`, stamp(time.Now().UTC()), id); err != nil {
			return fmt.Errorf("soft delete %s: %w", id, err)
		}
		return nil
	})
}

// RestoreItem brings a soft-deleted item back and reindexes it.
func (s *Store) RestoreItem(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		item, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if item.DeletedAt == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE items SET deleted_at = NULL WHERE id = ?`, id); err != nil {
			return fmt.Errorf("restore %s: %w", id, err)
		}
		if err := ftsInsert(ctx, tx, item); err != nil {
			return err
		}
		return nil
	})
}

// PurgeItem is the hard delete the 30-day sweep and "delete permanently" use.
// Children go by cascade; FTS is cleaned first with the old values.
func (s *Store) PurgeItem(ctx context.Context, id string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		item, err := getItemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := ftsDelete(ctx, tx, item); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("purge %s: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// TagsForItem returns the item's tag names, sorted for a stable UI.
func (s *Store) TagsForItem(ctx context.Context, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT t.name FROM item_tags it JOIN tags t ON t.id = it.tag_id WHERE it.item_id = ? ORDER BY t.name`, id)
	if err != nil {
		return nil, fmt.Errorf("tags for %s: %w", id, err)
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

// InReadLater reports membership of the seeded system space. It is a plain bool
// rather than an error the callers ignore.
func (s *Store) InReadLater(ctx context.Context, itemID string) bool {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM space_items si JOIN spaces sp ON sp.id = si.space_id
		 WHERE sp.slug = 'read-later' AND si.item_id = ?`, itemID).Scan(&n)
	return err == nil && n > 0
}

// --- row plumbing -----------------------------------------------------------

func insertItem(ctx context.Context, tx *sql.Tx, m model.Item) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO items (
		seq, id, url, url_original, title, note, kind, kind_source, summary,
		content_text, content_md, details_json, og_json, tags_text, source, import_ref,
		state, priority, attempts, next_attempt_at, last_error, prompt_version,
		extract_version, extract_error, enriched_at, transcript_text, transcript_state,
		transcript_lang, transcript_fetched_at, link_status, last_checked_at, deleted_at,
		created_at, saved_at, updated_at)
		VALUES (NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.URL, m.URLOriginal, m.Title, m.Note, string(m.Kind), string(m.KindSource), m.Summary,
		m.ContentText, m.ContentMD, m.DetailsJSON, m.OGJSON, string(m.Source), m.ImportRef,
		string(m.State), m.Priority, m.Attempts, nullableStamp(m.NextAttemptAt), m.LastError, m.PromptVersion,
		m.ExtractVersion, m.ExtractError, nullableStamp(m.EnrichedAt), m.TranscriptText, m.TranscriptState,
		m.TranscriptLang, nullableStamp(m.TranscriptFetchedAt), m.LinkStatus, nullableStamp(m.LastCheckedAt), nullableStamp(m.DeletedAt),
		stamp(m.CreatedAt), stamp(m.SavedAt), stamp(m.UpdatedAt))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateURL
		}
		return fmt.Errorf("insert item %s: %w", m.ID, err)
	}
	return nil
}

func updateItemRow(ctx context.Context, tx *sql.Tx, m model.Item) error {
	_, err := tx.ExecContext(ctx, `UPDATE items SET
		url = ?, url_original = ?, title = ?, note = ?, kind = ?, kind_source = ?, summary = ?,
		content_text = ?, content_md = ?, details_json = ?, og_json = ?, source = ?, import_ref = ?,
		state = ?, priority = ?, attempts = ?, next_attempt_at = ?, last_error = ?, prompt_version = ?,
		extract_version = ?, extract_error = ?, enriched_at = ?, transcript_text = ?, transcript_state = ?,
		transcript_lang = ?, transcript_fetched_at = ?, link_status = ?, last_checked_at = ?, deleted_at = ?,
		created_at = ?, saved_at = ?, updated_at = ?
		WHERE id = ?`,
		m.URL, m.URLOriginal, m.Title, m.Note, string(m.Kind), string(m.KindSource), m.Summary,
		m.ContentText, m.ContentMD, m.DetailsJSON, m.OGJSON, string(m.Source), m.ImportRef,
		string(m.State), m.Priority, m.Attempts, nullableStamp(m.NextAttemptAt), m.LastError, m.PromptVersion,
		m.ExtractVersion, m.ExtractError, nullableStamp(m.EnrichedAt), m.TranscriptText, m.TranscriptState,
		m.TranscriptLang, nullableStamp(m.TranscriptFetchedAt), m.LinkStatus, nullableStamp(m.LastCheckedAt), nullableStamp(m.DeletedAt),
		stamp(m.CreatedAt), stamp(m.SavedAt), stamp(m.UpdatedAt), m.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateURL
		}
		return fmt.Errorf("update item %s: %w", m.ID, err)
	}
	return nil
}

func getItemTx(ctx context.Context, tx *sql.Tx, id string) (model.Item, error) {
	return scanItemRow(tx.QueryRowContext(ctx, `SELECT `+itemColumns+` FROM items WHERE id = ?`, id))
}

func lastSeq(ctx context.Context, tx *sql.Tx) (int64, error) {
	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT last_insert_rowid()`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("read the new row's seq: %w", err)
	}
	return seq, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanItemRow(row rowScanner) (model.Item, error) {
	item, err := scanItem(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Item{}, ErrNotFound
	}
	return item, err
}

func scanItem(row rowScanner) (model.Item, error) {
	var (
		m        model.Item
		url      sql.NullString
		next     sql.NullString
		enriched sql.NullString
		tFetch   sql.NullString
		checked  sql.NullString
		deleted  sql.NullString
		created  string
		saved    string
		updated  string
		kind     string
		kindSrc  string
		source   string
		state    string
	)
	err := row.Scan(&m.Seq, &m.ID, &url, &m.URLOriginal, &m.Title, &m.Note, &kind, &kindSrc, &m.Summary,
		&m.ContentText, &m.ContentMD, &m.DetailsJSON, &m.OGJSON, &m.TagsText, &source, &m.ImportRef,
		&state, &m.Priority, &m.Attempts, &next, &m.LastError, &m.PromptVersion,
		&m.ExtractVersion, &m.ExtractError, &enriched, &m.TranscriptText, &m.TranscriptState,
		&m.TranscriptLang, &tFetch, &m.LinkStatus, &checked, &deleted,
		&created, &saved, &updated)
	if err != nil {
		return model.Item{}, err
	}
	if url.Valid {
		v := url.String
		m.URL = &v
	}
	m.Kind = model.Kind(kind)
	m.KindSource = model.KindSource(kindSrc)
	m.Source = model.Source(source)
	m.State = model.State(state)
	if m.NextAttemptAt, err = nullableTime(next); err != nil {
		return model.Item{}, err
	}
	if m.EnrichedAt, err = nullableTime(enriched); err != nil {
		return model.Item{}, err
	}
	if m.TranscriptFetchedAt, err = nullableTime(tFetch); err != nil {
		return model.Item{}, err
	}
	if m.LastCheckedAt, err = nullableTime(checked); err != nil {
		return model.Item{}, err
	}
	if m.DeletedAt, err = nullableTime(deleted); err != nil {
		return model.Item{}, err
	}
	if m.CreatedAt, err = parseStamp(created); err != nil {
		return model.Item{}, err
	}
	if m.SavedAt, err = parseStamp(saved); err != nil {
		return model.Item{}, err
	}
	if m.UpdatedAt, err = parseStamp(updated); err != nil {
		return model.Item{}, err
	}
	return m, nil
}

// --- tags -------------------------------------------------------------------

// syncTags unions the incoming names into the item's tags. Tag normalisation
// proper is phase 9; today it trims and ignores duplicates.
func syncTags(ctx context.Context, tx *sql.Tx, itemID string, names []string) error {
	for _, name := range normaliseTags(names) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO tags (name) VALUES (?) ON CONFLICT(name) DO NOTHING`, name); err != nil {
			return fmt.Errorf("tag %q: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO item_tags (item_id, tag_id, source)
			 SELECT ?, id, 'manual' FROM tags WHERE name = ?
			 ON CONFLICT(item_id, tag_id) DO NOTHING`, itemID, name); err != nil {
			return fmt.Errorf("link tag %q: %w", name, err)
		}
	}
	return nil
}

// refreshTagsText keeps the denormalised FTS column in step; design.md §8 says
// typing "rust" has to find items tagged rust.
func refreshTagsText(ctx context.Context, tx *sql.Tx, seq int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE items SET tags_text = COALESCE((
		SELECT group_concat(t.name, ', ') FROM (
			SELECT t.name FROM item_tags it JOIN tags t ON t.id = it.tag_id
			WHERE it.item_id = (SELECT id FROM items WHERE seq = ?) ORDER BY t.name
		) t), '') WHERE seq = ?`, seq, seq)
	if err != nil {
		return fmt.Errorf("refresh tags_text for seq %d: %w", seq, err)
	}
	return nil
}

func addToReadLater(ctx context.Context, tx *sql.Tx, itemID string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO space_items (space_id, item_id)
		 SELECT id, ? FROM spaces WHERE slug = 'read-later'
		 ON CONFLICT(space_id, item_id) DO NOTHING`, itemID)
	if err != nil {
		return fmt.Errorf("add %s to Read later: %w", itemID, err)
	}
	return nil
}

func normaliseTags(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, name := range in {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// --- FTS (design.md §7: external content, explicit writes) ------------------

// The values are passed explicitly rather than re-read with a subquery for one
// reason: the note we index is not byte-identical to the note we store. Merge
// stamps an appended note with an HTML comment, and "saved" must not become a
// search term. Insert and delete have to apply the same transform or FTS5's
// external-content bookkeeping drifts.
func ftsInsert(ctx context.Context, tx *sql.Tx, m model.Item) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO items_fts
		(rowid, title, summary, content_text, transcript_text, note, tags_text)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.Seq, m.Title, m.Summary, m.ContentText, m.TranscriptText, searchableNote(m.Note), m.TagsText)
	if err != nil {
		return fmt.Errorf("index item seq %d: %w", m.Seq, err)
	}
	return nil
}

// ftsDelete retracts an external-content row and therefore needs the OLD
// values: callers pass the item as it was read, before the write that changed
// it (design.md §7).
func ftsDelete(ctx context.Context, tx *sql.Tx, m model.Item) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO items_fts
		(items_fts, rowid, title, summary, content_text, transcript_text, note, tags_text)
		VALUES ('delete', ?, ?, ?, ?, ?, ?, ?)`,
		m.Seq, m.Title, m.Summary, m.ContentText, m.TranscriptText, searchableNote(m.Note), m.TagsText)
	if err != nil {
		return fmt.Errorf("unindex item seq %d: %w", m.Seq, err)
	}
	return nil
}

// searchableNote drops the merge's timestamp comments: they are provenance for
// a human reading the row, not text worth searching.
func searchableNote(note string) string {
	if !strings.Contains(note, "<!--") {
		return note
	}
	var b strings.Builder
	for _, segment := range strings.Split(note, noteSeparator) {
		if b.Len() > 0 {
			b.WriteString(noteSeparator)
		}
		b.WriteString(stripStampComment(segment))
	}
	return b.String()
}

// --- the note merge rule (design.md §5) -------------------------------------

// noteSeparator opens the appended chunk. The timestamp rides inside an HTML
// comment so the merged note stays readable Markdown and the trail survives.
const noteSeparator = "\n\n---\n\n"

// appendNote appends incoming to existing, separated and timestamped, unless the
// same note (trimmed) is already there — plans/02 test 2.
func appendNote(existing, incoming string, at time.Time) string {
	inc := strings.TrimSpace(incoming)
	if inc == "" {
		return existing
	}
	if noteAlreadyPresent(existing, inc) {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return inc
	}
	return strings.TrimRight(existing, "\n") + noteSeparator +
		"<!-- saved " + at.UTC().Format(time.RFC3339) + " -->\n\n" + inc
}

// noteAlreadyPresent compares trimmed segment text, ignoring the timestamp
// comment this package writes in front of an appended note.
func noteAlreadyPresent(existing, note string) bool {
	for _, segment := range strings.Split(existing, "---") {
		if strings.TrimSpace(stripStampComment(segment)) == note {
			return true
		}
	}
	return false
}

func stripStampComment(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "<!--") {
		end := strings.Index(s, "-->")
		if end < 0 {
			break
		}
		s = strings.TrimSpace(s[end+3:])
	}
	return s
}

// --- validation and timestamps ---------------------------------------------

func validate(in ItemInput) error {
	if in.URL != nil && strings.TrimSpace(*in.URL) == "" {
		return errors.New("store: an empty URL is a note, send nil")
	}
	if in.Kind != "" && !in.Kind.Valid() {
		return fmt.Errorf("store: %q is not a kind", in.Kind)
	}
	if in.KindSource != "" && !in.KindSource.Valid() {
		return fmt.Errorf("store: %q is not a kind source", in.KindSource)
	}
	if in.Source != "" && !in.Source.Valid() {
		return fmt.Errorf("store: %q is not a source", in.Source)
	}
	return nil
}

func kindOrDefault(k model.Kind, url *string) model.Kind {
	if k != "" {
		return k
	}
	if url == nil {
		return model.KindNote // a URL-less item is written as note (design.md §7)
	}
	return model.KindArticle
}

func kindSourceOrDefault(k model.KindSource) model.KindSource {
	if k != "" {
		return k
	}
	return model.KindSourceDefault
}

func sourceOrDefault(s model.Source) model.Source {
	if s != "" {
		return s
	}
	return model.SourceApp
}

func jsonOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// tsLayout is what every timestamp this package writes looks like: UTC, fixed
// width, so string ordering in SQLite is chronological ordering.
const tsLayout = "2006-01-02T15:04:05.000000000Z"

func stamp(t time.Time) string { return t.UTC().Format(tsLayout) }

func nullableStamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return stamp(*t)
}

func parseStamp(s string) (time.Time, error) {
	for _, layout := range []string{tsLayout, time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("store: cannot parse timestamp %q", s)
}

func nullableTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil, nil
	}
	t, err := parseStamp(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
