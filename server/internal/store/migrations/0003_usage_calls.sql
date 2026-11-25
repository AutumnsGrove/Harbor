-- 0003: what the AI cost, one row per billed call (plans/03, issue #52).
--
-- items already record what the model answered. This table is cost metadata and
-- nothing else: no page text, no prompt, no title, no note. It is written from
-- the same place that logs a `nova call`, so the panel and the log line agree.
--
-- A row is written when the provider answered and reported a cost, even if the
-- answer was later rejected: that call was billed. A call that never reached the
-- provider writes nothing.
CREATE TABLE usage_calls (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  at                TEXT    NOT NULL,            -- RFC3339 UTC, the moment of the call
  surface           TEXT    NOT NULL,            -- classify | write | embed
  requested_model   TEXT    NOT NULL,
  served_model      TEXT    NOT NULL DEFAULT '',
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd          REAL    NOT NULL DEFAULT 0,
  item_id           TEXT                         -- NULL when the caller had no item
);

-- The panel's window is "since an instant", so `at` is the index that matters.
CREATE INDEX usage_calls_at ON usage_calls(at);
