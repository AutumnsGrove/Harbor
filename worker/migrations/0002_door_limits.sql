-- Fixed-window door dampening.
--
-- Why this is in D1: the Workers rate-limit binding always answers success:true
-- on live Workers (workers-sdk#16097, reproduced 2026-11-19) and the Cache API
-- is a no-op on workers.dev, so neither can actually dampen anything. One row
-- per (door+caller, window) is cheap at this scale and works everywhere.
--
-- The write happens inside the request, but it is best-effort: if it fails the
-- save still goes through (design.md §1: the save path never fails).

CREATE TABLE door_limits (
  key        TEXT NOT NULL,   -- "<source>:<ip>"
  window     INTEGER NOT NULL,-- unix seconds / period
  hits       INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (key, window)
);

CREATE INDEX door_limits_old ON door_limits(window);
