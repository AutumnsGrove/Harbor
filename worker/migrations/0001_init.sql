-- Phase 1: the queue, its blobs, and the hashed door tokens.
--
-- Everything here is append-only from the public side. A door token can insert a
-- job and an R2 object; only the potato-scope credential can claim, read or
-- close one (design.md §4). Applied with `wrangler d1 migrations apply harbor`.

CREATE TABLE jobs (
  id           TEXT PRIMARY KEY,                 -- uuid v4, minted by the Worker
  source       TEXT NOT NULL,                    -- telegram|shortcut (in-app saves never touch the Worker)
  kind         TEXT NOT NULL,                    -- link|note|upload
  link         TEXT,                             -- raw, uncanonicalised (the potato owns canonicalisation)
  note         TEXT,
  title        TEXT,
  payload_json TEXT NOT NULL DEFAULT '{}',       -- source-specific extras (telegram ids)
  group_key    TEXT,                             -- "<chat_id>:<media_group_id>"; NULL for everything else
  touched_at   INTEGER NOT NULL,                 -- creation and album append; claim/lease never touch it
  status       TEXT NOT NULL DEFAULT 'queued',   -- queued|claimed|done|failed
  attempts     INTEGER NOT NULL DEFAULT 0,
  claimed_at   INTEGER,                          -- unix seconds; lease anchor
  lease_until  INTEGER,                          -- claimed_at + lease
  last_error   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  done_at      INTEGER
);

CREATE INDEX jobs_claimable ON jobs(status, created_at);
CREATE UNIQUE INDEX jobs_group ON jobs(group_key) WHERE group_key IS NOT NULL;
CREATE INDEX jobs_lease ON jobs(lease_until) WHERE status = 'claimed';
CREATE INDEX jobs_done ON jobs(done_at) WHERE status = 'done';

-- One row per file, so a Telegram album can carry several. `n` is caller-supplied
-- and never "count + 1": upload = 0, Telegram = message_id, so concurrent album
-- appends cannot race on the next index.
CREATE TABLE blobs (
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  n            INTEGER NOT NULL,
  r2_key       TEXT NOT NULL,
  content_type TEXT NOT NULL,
  bytes        INTEGER NOT NULL,
  created_at   INTEGER NOT NULL,
  PRIMARY KEY (job_id, n)
);

CREATE INDEX blobs_key ON blobs(r2_key);

-- Hashed at rest, one row per door so they revoke independently. `scope` is what
-- makes a leaked door token append-only: it never reaches a potato route.
CREATE TABLE tokens (
  id           TEXT PRIMARY KEY,                 -- telegram|shortcut|potato
  scope        TEXT NOT NULL,                    -- door|potato
  sha256       TEXT NOT NULL,                    -- hex of the bearer token
  label        TEXT NOT NULL DEFAULT '',
  last_seen_at INTEGER,
  created_at   INTEGER NOT NULL,
  revoked_at   INTEGER
);

CREATE UNIQUE INDEX tokens_hash ON tokens(sha256);
