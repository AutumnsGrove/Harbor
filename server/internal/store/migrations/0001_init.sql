-- 0001_init: the whole schema from docs/design.md section 7, verbatim.
-- Everything between the markers below is copied by script, not retyped, and
-- store/schema_test.go fails if it stops matching the design. Later phases only
-- ALTER (new numbered migrations), never edit this file.
-- Foreign keys are enforced per connection (PRAGMA foreign_keys=ON in the DSN).

-- BEGIN design.md section 7
-- Migration 0001 creates every table below (schema reconciled 2026-11-04). Later phases only ALTER
-- to add a column, and the phase plan says so. Run with PRAGMA foreign_keys=ON.
CREATE TABLE items (
  seq             INTEGER PRIMARY KEY AUTOINCREMENT, -- FTS5 rowid; AUTOINCREMENT = never reused
  id              TEXT NOT NULL UNIQUE,              -- the public id (uuid)
  url             TEXT,                              -- canonicalized; NULL for pure notes
  url_original    TEXT NOT NULL DEFAULT '',          -- exactly what was saved, pre-canonicalisation
  title           TEXT NOT NULL DEFAULT '',
  note            TEXT NOT NULL DEFAULT '',          -- the user's own words (Markdown)
  kind            TEXT NOT NULL DEFAULT 'article',   -- one of the 12 kinds; never 'unknown'
                                                     -- (URL-less notes are written as 'note')
  kind_source     TEXT NOT NULL DEFAULT 'default',   -- default|ai|manual; manual is never overwritten
  summary         TEXT NOT NULL DEFAULT '',
  content_text    TEXT NOT NULL DEFAULT '',          -- extracted plain text, feeds FTS5
  content_md      TEXT NOT NULL DEFAULT '',          -- extracted Markdown; the reader renders this
  details_json    TEXT NOT NULL DEFAULT '{}',        -- per-kind fields from JSON-LD (recipe, product, place…)
  og_json         TEXT NOT NULL DEFAULT '{}',        -- cover, description, site, author, published, excerpt
  tags_text       TEXT NOT NULL DEFAULT '',          -- denormalised tag names, for FTS5 only
  source          TEXT NOT NULL DEFAULT 'app',       -- app|telegram|shortcut|quick|share|import
  import_ref      TEXT NOT NULL DEFAULT '',          -- Raindrop id / bookmark path, for re-import
  -- enrichment queue (the potato's queue; the Worker's job is already done by now)
  state           TEXT NOT NULL DEFAULT 'queued',    -- queued|enriching|ready|failed
  priority        INTEGER NOT NULL DEFAULT 0,        -- 0 interactive save; -1 bulk/import backlog
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at DATETIME,
  last_error      TEXT NOT NULL DEFAULT '',
  prompt_version  TEXT NOT NULL DEFAULT '',
  extract_version TEXT NOT NULL DEFAULT '',          -- ties to highlights.anchor_json.text_hash
  extract_error   TEXT NOT NULL DEFAULT '',
  enriched_at     DATETIME,
  -- transcripts
  transcript_text  TEXT NOT NULL DEFAULT '',         -- captions/transcript, feeds FTS5
  transcript_state TEXT NOT NULL DEFAULT '',         -- ''|ok|unavailable|error|too_large
  transcript_lang  TEXT NOT NULL DEFAULT '',
  transcript_fetched_at DATETIME,
  -- link health
  link_status     TEXT NOT NULL DEFAULT 'ok',        -- ok|dead|error (scheduled checker)
  last_checked_at DATETIME,
  deleted_at      DATETIME,                          -- NULL = live; purged after 30 days
  -- three clocks: when it was made, when it was last saved, when it was last edited
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, -- original date (import preserves it)
  saved_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, -- last save; re-save bumps; home sorts on this
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP  -- last edit
);
-- Includes soft-deleted rows on purpose: re-saving a deleted URL restores + merges (§5).
CREATE UNIQUE INDEX items_by_url ON items(url) WHERE url IS NOT NULL AND url <> '';
CREATE INDEX items_by_saved ON items(saved_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX items_queue ON items(state, priority DESC, next_attempt_at);

CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
-- A merge leaves the old spelling behind as an alias, so a re-import or a
-- future AI tag maps back to the canonical tag instead of recreating it.
CREATE TABLE tag_aliases (
  alias  TEXT PRIMARY KEY,
  tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE
);
CREATE TABLE item_tags (
  item_id TEXT    NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
  source  TEXT    NOT NULL DEFAULT 'ai',  -- ai|manual
  PRIMARY KEY (item_id, tag_id)
);
CREATE INDEX item_tags_by_tag ON item_tags(tag_id);

-- Originals live on the potato; R2 is a mirror. One row per item attachment; the file is shared by sha256.
CREATE TABLE media (
  id            TEXT PRIMARY KEY,
  item_id       TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL,              -- image|pdf|file (thumbnails are derived files, not rows)
  filename      TEXT NOT NULL DEFAULT '',
  content_type  TEXT NOT NULL DEFAULT '',
  bytes         INTEGER NOT NULL DEFAULT 0,
  sha256        TEXT NOT NULL,              -- content address + mirror integrity
  width         INTEGER,
  height        INTEGER,
  local_path    TEXT NOT NULL,              -- relative to data/media
  r2_key        TEXT NOT NULL DEFAULT '',   -- set once mirrored (or the incoming/ key for remote saves)
  pdf_text_state TEXT NOT NULL DEFAULT '',  -- ''|text|ocr|empty
  derived_at    DATETIME,                   -- thumbnail generation time, for the re-derive sweep
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX media_by_item ON media(item_id);
CREATE INDEX media_by_sha ON media(sha256);   -- reference counting before any unlink or R2 delete

-- Reader highlights; anchor is a text-position selector, not a CSS path.
CREATE TABLE highlights (
  id          TEXT PRIMARY KEY,
  item_id     TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  quote       TEXT NOT NULL,
  note        TEXT NOT NULL DEFAULT '',    -- annotation on the highlight
  color       TEXT NOT NULL DEFAULT 'amber', -- amber|magenta|teal|blue
  anchor_json TEXT NOT NULL,               -- quote + prefix/suffix + ordinal + text_hash (+ source)
  detached    INTEGER NOT NULL DEFAULT 0,  -- 1 = could not be re-anchored; shown separately
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX highlights_by_item ON highlights(item_id);

-- Smart Spaces: 'auto' is rule-driven, 'saved' is an explicit query,
-- 'manual' is hand-curated (Read later, imported Raindrop collections).
CREATE TABLE spaces (
  id         TEXT PRIMARY KEY,
  slug       TEXT UNIQUE,                  -- non-NULL only for system spaces; 0001 seeds 'read-later'
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL DEFAULT 'auto',
  rule_json  TEXT NOT NULL DEFAULT '{}',   -- see open-questions H3 for the exact vocabulary
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE space_items (
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  item_id  TEXT NOT NULL REFERENCES items(id)  ON DELETE CASCADE,
  PRIMARY KEY (space_id, item_id)
);
CREATE INDEX space_items_by_item ON space_items(item_id);
-- AI suggestions: never become Spaces by themselves; dismissals are remembered by fingerprint.
CREATE TABLE space_suggestions (
  id          TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL UNIQUE,        -- hash of the suggested rule
  label       TEXT NOT NULL,
  rule_json   TEXT NOT NULL,
  state       TEXT NOT NULL DEFAULT 'pending', -- pending|accepted|dismissed
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Worker-rendered snapshots (never stored locally; referenced by key). Latest copy only.
CREATE TABLE archives (
  item_id        TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  html_key       TEXT NOT NULL DEFAULT '',
  screenshot_key TEXT NOT NULL DEFAULT '',
  pdf_key        TEXT NOT NULL DEFAULT '',   -- on demand
  bytes          INTEGER NOT NULL DEFAULT 0,
  content_hash   TEXT NOT NULL DEFAULT '',   -- detect a changed page on re-archive
  created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Written explicitly in the same transaction as the item (no triggers). Because it is an external
-- content table, every update/delete must first issue the FTS 'delete' command with the OLD column
-- values, so the store reads the old row inside the transaction.
CREATE VIRTUAL TABLE items_fts USING fts5(
  title, summary, content_text, transcript_text, note, tags_text,
  content='items', content_rowid='seq'
);

CREATE TABLE item_embeddings (
  item_id      TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  model        TEXT NOT NULL,        -- e.g. perplexity/pplx-embed-v1-0.6b
  dims         INTEGER NOT NULL,     -- 1024 for pplx-embed-v1-0.6b; stored, never assumed
  content_hash TEXT NOT NULL,        -- skip re-embedding unchanged rows
  vec          BLOB NOT NULL,        -- float32 little-endian; no extension needed
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Bookkeeping
CREATE TABLE processed_jobs (        -- Worker job ids already ingested: makes ingest/replay idempotent
  job_id  TEXT PRIMARY KEY,
  item_id TEXT,
  done_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE import_runs (           -- resumable imports; the canonical URL set is the idempotence key
  id TEXT PRIMARY KEY, file_sha256 TEXT NOT NULL, state TEXT NOT NULL,
  checkpoint INTEGER NOT NULL DEFAULT 0, counts_json TEXT NOT NULL DEFAULT '{}',
  started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, finished_at DATETIME
);
CREATE TABLE task_runs (             -- the in-process scheduler's memory, so a reboot never skips a day
  name TEXT PRIMARY KEY, last_run_at DATETIME, last_ok_at DATETIME, last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at DATETIME NOT NULL);
-- END design.md section 7

-- Seeded system space (plans/02-core-library.md, "Data model notes"): the
-- composer's Read later switch and merge's "keep Read later" point at it.
INSERT INTO spaces (id, slug, name, kind) VALUES ('read-later', 'read-later', 'Read later', 'manual');
