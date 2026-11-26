// Package spaces is Smart Spaces: the rule vocabulary from open-questions H3, the
// evaluation of a rule into typed SQL, and the pattern detection that can suggest
// a Space without ever creating one.
//
// The vocabulary is the contract. A rule is stored as JSON in spaces.rule_json and
// may outlive the code that wrote it, so an unknown field is an error rather than
// something to ignore: a stale client asking for `within` instead of `within_days`
// must fail loudly rather than quietly match everything.
package spaces

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Bounds on the stored rule. A Space is a saved query a human typed, not a
// document: these keep one row from becoming a payload and keep the generated
// SQL to a size the planner can use.
const (
	MaxTags       = 32
	MaxTextBytes  = 256
	MaxWithinDays = 3650 // ten years; a larger window is "everything", not a rule
)

// dateLayout is the shape a rule stores a date in. A full timestamp is accepted
// too, because the UI may one day send one, but the date alone is enough for
// before/after and reads better in rule_json.
const dateLayout = "2006-01-02"

// ErrInvalidRule is the shape of every rejection, so a caller can tell a bad rule
// from a database failure.
var ErrInvalidRule = errors.New("spaces: invalid rule")

// Rule is the H3 vocabulary. Fields AND together; a list ORs within itself; an
// empty rule matches everything.
//
// Pointers on the numbers distinguish "not set" from "set to zero": within_days: 0
// is a mistake to reject, not a rule that matches nothing.
type Rule struct {
	TagsAll       []string       `json:"tags_all,omitempty"`
	TagsAny       []string       `json:"tags_any,omitempty"`
	Kinds         []model.Kind   `json:"kinds,omitempty"`
	Sources       []model.Source `json:"sources,omitempty"`
	WithinDays    *int           `json:"within_days,omitempty"`
	After         string         `json:"after,omitempty"`
	Before        string         `json:"before,omitempty"`
	Text          string         `json:"text,omitempty"`
	SimilarToItem string         `json:"similar_to_item,omitempty"`
}

// Parse reads a stored rule. Unknown fields and trailing data are errors: rule_json
// is written by this package and read by it, and anything else is a client from a
// different version.
//
// Field names match case-insensitively, which is encoding/json's behaviour and not
// worth fighting: `Tags_All` and `tags_all` mean the same field, so no rule can say
// something different from what it appears to. An unknown *name* is still rejected
// (`within`, `similar_to`), which is the case that would silently change meaning.
func Parse(raw []byte) (Rule, error) {
	var rule Rule
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rule); err != nil {
		// The decoder's message names the field for a DisallowUnknownFields
		// rejection, which is the part a person can act on.
		return Rule{}, fmt.Errorf("%w: %v", ErrInvalidRule, err)
	}
	if decoder.More() {
		return Rule{}, fmt.Errorf("%w: trailing data after the rule", ErrInvalidRule)
	}
	return rule, rule.Validate()
}

// Marshal is the storage form, normalised by Validate first so a Space can never
// be written unvalidated.
func (r Rule) Marshal() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

// Empty reports whether the rule constrains nothing. An empty Space is legal (it
// is "everything, as a saved view"), but the UI says so rather than showing a
// filter nobody set.
func (r Rule) Empty() bool {
	return len(r.TagsAll) == 0 && len(r.TagsAny) == 0 && len(r.Kinds) == 0 &&
		len(r.Sources) == 0 && r.WithinDays == nil && r.After == "" && r.Before == "" &&
		strings.TrimSpace(r.Text) == "" && r.SimilarToItem == ""
}

// Validate normalises the rule in place and rejects what cannot be evaluated.
// Normalising means: tag names trimmed and lowercased (the canonical form the
// write path stores), dates parsed and re-rendered, text trimmed.
func (r *Rule) Validate() error {
	r.TagsAll = normaliseTags(r.TagsAll)
	r.TagsAny = normaliseTags(r.TagsAny)
	if len(r.TagsAll) > MaxTags || len(r.TagsAny) > MaxTags {
		return fmt.Errorf("%w: at most %d tags in a field", ErrInvalidRule, MaxTags)
	}
	// A blank tag is a mistake, not a filter: it would compile to `t.name = ''`
	// and match nothing, which is the silent failure this package exists to avoid
	// (review on #94).
	for _, field := range []struct {
		name string
		tags []string
	}{
		{"tags_all", r.TagsAll},
		{"tags_any", r.TagsAny},
	} {
		for _, tag := range field.tags {
			if tag == "" {
				return fmt.Errorf("%w: %s holds an empty tag", ErrInvalidRule, field.name)
			}
		}
	}
	for _, kind := range r.Kinds {
		if !kind.Valid() {
			return fmt.Errorf("%w: %q is not one of the twelve kinds", ErrInvalidRule, kind)
		}
	}
	for _, source := range r.Sources {
		if !source.Valid() {
			return fmt.Errorf("%w: %q is not a source", ErrInvalidRule, source)
		}
	}
	if r.WithinDays != nil {
		if *r.WithinDays <= 0 || *r.WithinDays > MaxWithinDays {
			return fmt.Errorf("%w: within_days must be between 1 and %d", ErrInvalidRule, MaxWithinDays)
		}
	}
	after, err := normaliseDate(r.After)
	if err != nil {
		return fmt.Errorf("%w: after: %v", ErrInvalidRule, err)
	}
	r.After = after
	before, err := normaliseDate(r.Before)
	if err != nil {
		return fmt.Errorf("%w: before: %v", ErrInvalidRule, err)
	}
	r.Before = before
	if r.After != "" && r.Before != "" && r.Before <= r.After {
		// before is exclusive, so equal bounds match nothing; that is a typo, not
		// a Space somebody wants.
		return fmt.Errorf("%w: before %s is not after after %s", ErrInvalidRule, r.Before, r.After)
	}
	r.Text = strings.TrimSpace(r.Text)
	if len(r.Text) > MaxTextBytes {
		return fmt.Errorf("%w: text is longer than %d bytes", ErrInvalidRule, MaxTextBytes)
	}
	if r.Text != "" {
		// Text that searches for nothing — "---", "!!!" — must be rejected when
		// the rule is written, not when it is read. A Space that fails on every
		// evaluation is a Space the owner cannot fix, and the error would look like
		// a database fault rather than a bad rule (review on #94).
		if _, err := store.FTSQuery(r.Text); err != nil {
			return fmt.Errorf("%w: text has nothing to search for: %v", ErrInvalidRule, err)
		}
	}
	r.SimilarToItem = strings.TrimSpace(r.SimilarToItem)
	if len(r.SimilarToItem) > 64 {
		return fmt.Errorf("%w: similar_to_item is not an item id", ErrInvalidRule)
	}
	return nil
}

// normaliseTags trims and lowercases. An empty name is rejected by Validate
// rather than dropped here: a tag rule with a blank tag is a mistake the UI should
// show, not one to quietly fix.
func normaliseTags(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		out = append(out, strings.ToLower(strings.TrimSpace(name)))
	}
	return out
}

// normaliseDate accepts a date or an RFC3339 timestamp and returns the date in
// UTC. A date is what a rule means; the time of day is the UI's business.
func normaliseDate(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC().Format(dateLayout), nil
	}
	parsed, err := time.Parse(dateLayout, raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a date (YYYY-MM-DD) or an RFC3339 timestamp", raw)
	}
	return parsed.Format(dateLayout), nil
}

// Similarity resolves "items like this one" to candidate ids. Phase 9 ships a scan
// over item_embeddings; phase 10 swaps in the vector cache without touching the
// rule code, which is why the rule never builds a WHERE clause from a vector.
type Similarity interface {
	// Neighbours returns the ids whose cosine to the given item is at or above the
	// configured threshold, nearest first. An unknown item is not an error: it is
	// an empty list, because a Space about a deleted item matches nothing.
	Neighbours(ctx context.Context, itemID string) ([]string, error)
}

// Clause is a WHERE fragment and its bound values, never a string with a value in
// it. An empty SQL means "no restriction": the caller appends it as-is, so a rule
// with no fields matches every item.
type Clause struct {
	SQL  string
	Args []any
}

// Where compiles the rule into SQL over the items table.
//
// now is passed in rather than read, so within_days is testable and a Space is
// evaluated against the request's clock rather than a frozen creation date.
func (r Rule) Where(ctx context.Context, now time.Time, sim Similarity) (Clause, error) {
	if err := r.Validate(); err != nil {
		return Clause{}, err
	}
	var (
		parts []string
		args  []any
	)

	for _, name := range r.TagsAll {
		parts = append(parts, "EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id"+
			" WHERE it.item_id = items.id AND t.name = ?)")
		args = append(args, name)
	}
	if len(r.TagsAny) > 0 {
		parts = append(parts, "EXISTS (SELECT 1 FROM item_tags it JOIN tags t ON t.id = it.tag_id"+
			" WHERE it.item_id = items.id AND t.name IN ("+placeholders(len(r.TagsAny))+"))")
		for _, name := range r.TagsAny {
			args = append(args, name)
		}
	}
	if len(r.Kinds) > 0 {
		parts = append(parts, "kind IN ("+placeholders(len(r.Kinds))+")")
		for _, kind := range r.Kinds {
			args = append(args, string(kind))
		}
	}
	if len(r.Sources) > 0 {
		parts = append(parts, "source IN ("+placeholders(len(r.Sources))+")")
		for _, source := range r.Sources {
			args = append(args, string(source))
		}
	}
	if r.WithinDays != nil {
		from := now.AddDate(0, 0, -*r.WithinDays).UTC()
		parts = append(parts, "saved_at >= ?")
		args = append(args, from.Format(time.RFC3339))
	}
	if r.After != "" {
		parts = append(parts, "saved_at >= ?")
		args = append(args, r.After)
	}
	if r.Before != "" {
		// Exclusive: a Space "before 2025" must not include 2025's own first item.
		parts = append(parts, "saved_at < ?")
		args = append(args, r.Before)
	}
	if r.Text != "" {
		query, err := store.FTSQuery(r.Text)
		if err != nil {
			return Clause{}, err
		}
		parts = append(parts, "seq IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)")
		args = append(args, query)
	}
	if r.SimilarToItem != "" {
		if sim == nil {
			return Clause{}, fmt.Errorf("%w: a rule with similar_to_item needs a similarity source", ErrInvalidRule)
		}
		ids, err := sim.Neighbours(ctx, r.SimilarToItem)
		if err != nil {
			return Clause{}, err
		}
		if len(ids) == 0 {
			// A Space about an item nothing resembles matches nothing, which is a
			// fact rather than an error. `0` is false in SQLite's WHERE.
			parts = append(parts, "0")
		} else {
			parts = append(parts, "id IN ("+placeholders(len(ids))+")")
			for _, id := range ids {
				args = append(args, id)
			}
		}
	}

	if len(parts) == 0 {
		return Clause{}, nil
	}
	return Clause{SQL: strings.Join(parts, " AND "), Args: args}, nil
}

// placeholders builds "?, ?, ?" for a bound list.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
