// Package model holds the shared types every later phase builds on: the rows
// of design.md §7 as Go structs, and the enums that constrain them. There is
// no SQL and no behaviour here; the store owns persistence, and the rules
// (canonicalisation, merge) live in their own packages.
//
// Each field's `db` tag is its column name. A test reads the DDL out of
// docs/design.md and compares, so the structs cannot drift from the schema.
package model

import "time"

// Item is one saved thing: a link, a note, or an upload.
type Item struct {
	Seq         int64   `db:"seq"` // FTS5 rowid; never reused
	ID          string  `db:"id"`  // the public id (uuid)
	URL         *string `db:"url"` // canonical; nil for a pure note
	URLOriginal string  `db:"url_original"`
	Title       string  `db:"title"`
	Note        string  `db:"note"`

	Kind       Kind       `db:"kind"`
	KindSource KindSource `db:"kind_source"`
	Summary    string     `db:"summary"`

	ContentText string `db:"content_text"`
	ContentMD   string `db:"content_md"`
	DetailsJSON string `db:"details_json"`
	OGJSON      string `db:"og_json"`
	TagsText    string `db:"tags_text"` // denormalised tag names, for FTS5 only

	Source    Source `db:"source"`
	ImportRef string `db:"import_ref"`

	// The potato's enrichment queue.
	State          State      `db:"state"`
	Priority       int        `db:"priority"` // 0 interactive, -1 import backlog
	Attempts       int        `db:"attempts"`
	NextAttemptAt  *time.Time `db:"next_attempt_at"`
	LastError      string     `db:"last_error"`
	PromptVersion  string     `db:"prompt_version"`
	ExtractVersion string     `db:"extract_version"`
	ExtractError   string     `db:"extract_error"`
	EnrichedAt     *time.Time `db:"enriched_at"`

	TranscriptText      string     `db:"transcript_text"`
	TranscriptState     string     `db:"transcript_state"`
	TranscriptLang      string     `db:"transcript_lang"`
	TranscriptFetchedAt *time.Time `db:"transcript_fetched_at"`

	LinkStatus    string     `db:"link_status"` // ok|dead|error
	LastCheckedAt *time.Time `db:"last_checked_at"`
	DeletedAt     *time.Time `db:"deleted_at"` // nil = live

	// Three clocks: made, last saved (home sorts on it), last edited.
	CreatedAt time.Time `db:"created_at"`
	SavedAt   time.Time `db:"saved_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// Tag is a canonical tag name. Aliases and item links are separate tables.
type Tag struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

// Media is one attachment of an item; the file is shared by sha256.
type Media struct {
	ID           string     `db:"id"`
	ItemID       string     `db:"item_id"`
	Kind         MediaKind  `db:"kind"`
	Filename     string     `db:"filename"`
	ContentType  string     `db:"content_type"`
	Bytes        int64      `db:"bytes"`
	SHA256       string     `db:"sha256"`
	Width        *int       `db:"width"`
	Height       *int       `db:"height"`
	LocalPath    string     `db:"local_path"` // relative to data/media
	R2Key        string     `db:"r2_key"`
	PDFTextState string     `db:"pdf_text_state"`
	DerivedAt    *time.Time `db:"derived_at"`
	CreatedAt    time.Time  `db:"created_at"`
}

// Highlight is a reader highlight anchored by text position, not CSS path.
type Highlight struct {
	ID         string         `db:"id"`
	ItemID     string         `db:"item_id"`
	Quote      string         `db:"quote"`
	Note       string         `db:"note"`
	Color      HighlightColor `db:"color"`
	AnchorJSON string         `db:"anchor_json"`
	Detached   bool           `db:"detached"` // true: could not be re-anchored
	CreatedAt  time.Time      `db:"created_at"`
}

// Space is a Smart Space: auto (rule-driven), saved (explicit query) or
// manual (hand-curated, including Read later).
type Space struct {
	ID        string    `db:"id"`
	Slug      *string   `db:"slug"` // non-nil only for system spaces such as read-later
	Name      string    `db:"name"`
	Kind      SpaceKind `db:"kind"`
	RuleJSON  string    `db:"rule_json"`
	CreatedAt time.Time `db:"created_at"`
}
