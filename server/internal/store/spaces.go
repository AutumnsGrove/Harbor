// Spaces: the manual ones the import creates from a folder, and membership.
//
// Phase 9 owns the Space editor, rules and saved queries. This file only holds
// what an import needs, so that a folder in an export becomes something the
// owner can open — and becomes it once, however many times the file is imported.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/google/uuid"
)

// EnsureManualSpace returns the manual Space with this name, creating it when
// there is none. The match ignores case and surrounding space, because an import
// run twice with "Recipes" and "recipes" must not leave two Spaces behind.
//
// Only manual Spaces are matched: a rule-driven Space that happens to share the
// folder's name chooses its members by rule, and filing items into it by hand
// would contradict that rule.
func (s *Store) EnsureManualSpace(ctx context.Context, name string) (model.Space, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return model.Space{}, errors.New("store: a Space needs a name")
	}

	space, err := scanSpaceRow(s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, kind, rule_json, created_at FROM spaces
		 WHERE kind = 'manual' AND lower(name) = lower(?) ORDER BY created_at, id LIMIT 1`, name))
	switch {
	case err == nil:
		return space, nil
	case !errors.Is(err, ErrNotFound):
		return model.Space{}, fmt.Errorf("looking for the manual Space %q: %w", name, err)
	}

	space = model.Space{
		ID:       uuid.NewString(),
		Name:     name,
		Kind:     model.SpaceManual,
		RuleJSON: "{}",
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO spaces (id, name, kind, rule_json) VALUES (?, ?, ?, ?)`,
		space.ID, space.Name, string(space.Kind), space.RuleJSON); err != nil {
		return model.Space{}, fmt.Errorf("creating the manual Space %q: %w", name, err)
	}
	return s.spaceByID(ctx, space.ID)
}

// AddItemToSpace files an item. It is idempotent, and it refuses unknown ids
// rather than dropping the call: the import counts the result, so a membership
// that did not happen has to be an error.
func (s *Store) AddItemToSpace(ctx context.Context, spaceID, itemID string) error {
	// The INSERT below would fail on the foreign keys anyway; the explicit
	// lookup is what makes the message say which id was wrong.
	if _, err := s.spaceByID(ctx, spaceID); err != nil {
		return err
	}
	if _, err := s.GetItem(ctx, itemID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO space_items (space_id, item_id) VALUES (?, ?)
		 ON CONFLICT(space_id, item_id) DO NOTHING`, spaceID, itemID); err != nil {
		return fmt.Errorf("filing %s in Space %s: %w", itemID, spaceID, err)
	}
	return nil
}

// SpaceBySlug reads a system Space — read-later, favourites — by the slug it was
// seeded with.
func (s *Store) SpaceBySlug(ctx context.Context, slug string) (model.Space, error) {
	return scanSpaceRow(s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, kind, rule_json, created_at FROM spaces WHERE slug = ?`, slug))
}

func (s *Store) spaceByID(ctx context.Context, id string) (model.Space, error) {
	return scanSpaceRow(s.db.QueryRowContext(ctx,
		`SELECT id, slug, name, kind, rule_json, created_at FROM spaces WHERE id = ?`, id))
}

func scanSpaceRow(row rowScanner) (model.Space, error) {
	var (
		space   model.Space
		slug    sql.NullString
		kind    string
		created string
	)
	err := row.Scan(&space.ID, &slug, &space.Name, &kind, &space.RuleJSON, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Space{}, ErrNotFound
	}
	if err != nil {
		return model.Space{}, err
	}
	if slug.Valid {
		space.Slug = &slug.String
	}
	space.Kind = model.SpaceKind(kind)
	if space.CreatedAt, err = parseStamp(created); err != nil {
		return model.Space{}, fmt.Errorf("space %s created_at: %w", space.ID, err)
	}
	return space, nil
}
