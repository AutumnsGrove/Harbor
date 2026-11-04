# Phase 1 — The Worker: a public door that can only append

**Status:** planned · **Design refs:** [§4](../design.md#4-architecture),
[§5](../design.md#5-save-path), [§11](../design.md#11-operations),
[open-questions B4/E1/E6/E7](../open-questions.md)
**Depends on:** phase 0 (CI, image) · **Gates:** [S1](spikes.md#s1--worker-streams-a-25-mb-upload-to-r2),
[S2](spikes.md#s2--d1-claim-by-update) · **Est:** 1–2 evenings

## Goal

The always-on front door: `POST /save` and `POST /upload` behind per-source tokens, a D1 job queue it
can only add to, R2 intake for blobs, presigned reads for the UI, and a cron that sweeps orphans.
Nothing behind it yet — the point of this phase is that a save is durable *before* any consumer
exists, and that the public surface has no read path at all.

The Worker is deliberately the smallest component. It has no database of the library, no models, and
no notion of an item.

## Scope

In: Hono app, D1 schema (`jobs`, `blobs`, `tokens`), R2 intake, token hashing/verification **with
scopes** (door vs potato), rate limiting, size/content-type caps, batch claim, pending, replay,
done/failed with lease, S3-style `/presign`, `/healthz`, orphan cron, `wrangler` config,
`contracts/` filled in, tests. The route list and scope of every route is
[design §4](../design.md#worker-api-and-token-scopes-settled-2026-11-04) — if a route is not in that
table, the table changes first.

Out (owned by later phases, listed so nobody discovers them mid-build): Telegram webhook and the
`/admin/tokens` routes (4), `PUT /mirror` (6), `DELETE /objects` purge (9), `POST /render` (11),
`/backups` (12). Anything that reads an item for the public.

## Deliverables

```
worker/
  wrangler.toml              D1 + R2 bindings, compat date, cron trigger
  migrations/0001_init.sql   jobs, tokens
  src/index.ts               routes + middleware
  src/auth.ts                token verify (WebCrypto SHA-256, constant-time compare) + scope check
                             (door vs potato); a route declares its required scope, default deny
  src/queue.ts               create · claim (batch, lease, album hold) · pending (read-only) ·
                             replay · done · failed · reclaim sweep
  src/blobs.ts               streamed put to incoming/<job>/<n>, size/content-type guards
  src/presign.ts             S3-style presigned GET (aws4fetch, read-only R2 credential as a Worker
                             secret); the Worker never proxies the bytes
  src/limits.ts              rate limit binding wrapper, caps as constants
  src/validate.ts            contracts/*.schema.json validation — @cfworker/json-schema or
                             precompiled validators; NOT Ajv's default (Workers forbid new Function)
  test/*.test.ts             vitest + miniflare, real D1/R2
  test/*.prop.ts             fast-check properties
contracts/
  save.schema.json           {link?, note?, title?}  (source comes from the token, never the body)
  upload.schema.json         raw-body metadata carried in headers/query (filename, content type, link?, note?, title?)
  job.schema.json            {id, source, kind, link?, note?, title?, group_key?, created_at,
                              blobs[{n,key,content_type,bytes}]}
  fixtures/*.json            required/optional/malformed cases used by both languages
```

## Data model (Worker-side only)

```sql
-- jobs: everything the potato pulls. Append-only from the public side.
CREATE TABLE jobs (
  id           TEXT PRIMARY KEY,          -- uuid v4
  source       TEXT NOT NULL,             -- telegram|shortcut (in-app, quick and share saves never touch the Worker)
  kind         TEXT NOT NULL,             -- link|note|upload
  link         TEXT,                      -- raw, uncanonicalised (the potato owns canonicalisation)
  note         TEXT,
  title        TEXT,
  payload_json TEXT NOT NULL DEFAULT '{}',-- source-specific extras (telegram ids)
  group_key    TEXT,                      -- Telegram "<chat_id>:<media_group_id>"; NULL for everything else
  touched_at   INTEGER NOT NULL,          -- set at creation and on every album blob append; claim and
                                          -- lease NEVER touch it (the album hold reads this, not updated_at)
  status       TEXT NOT NULL DEFAULT 'queued', -- queued|claimed|done|failed
  attempts     INTEGER NOT NULL DEFAULT 0,
  claimed_at   INTEGER,                   -- unix seconds; lease anchor
  lease_until  INTEGER,                   -- claimed_at + lease
  last_error   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  done_at      INTEGER
);
CREATE INDEX jobs_claimable ON jobs(status, created_at);
CREATE UNIQUE INDEX jobs_group ON jobs(group_key) WHERE group_key IS NOT NULL;  -- album upsert target
CREATE INDEX jobs_lease ON jobs(lease_until) WHERE status = 'claimed';

-- blobs are described in a child table so one job can carry an album.
CREATE TABLE blobs (
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  n            INTEGER NOT NULL,          -- caller-supplied, never "count + 1": upload = 0, Telegram =
                                          -- message_id, so concurrent album updates cannot race on the next index
  r2_key       TEXT NOT NULL,
  content_type TEXT NOT NULL,
  bytes        INTEGER NOT NULL,
  PRIMARY KEY (job_id, n)
);

-- tokens: hashed at rest, one row per door so they revoke independently.
CREATE TABLE tokens (
  id         TEXT PRIMARY KEY,            -- telegram|shortcut|potato
  scope      TEXT NOT NULL,               -- door|potato  (door: /save, /upload only)
  sha256     TEXT NOT NULL,               -- hex of the bearer token
  last_seen_at INTEGER,                   -- Settings → Sources shows a silently broken door
  label      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  revoked_at INTEGER
);
```

**Settled (design §4 job lifecycle, §13):** the lease. A potato that dies mid-job must not strand a
save, so `claim` sets `lease_until = now + 600`, and the cron returns expired claims to `queued`
(`attempts` already incremented). After `attempts >= 4`, status becomes `failed` and is *visible* in
the UI rather than silently retried forever. A job is done when the item is durably inserted on the
potato — not after enrichment. **Done jobs and their blobs are never deleted** by any cron, so
`GET /jobs/replay?since=` can re-serve them after a rollback or restore. The claim query is
`status='queued' AND (group_key IS NULL OR touched_at < now - 10)`: the ten-second hold applies to
Telegram albums only, never to an ordinary save.

## Tests first

1. **Contract fixtures validate.** Every fixture in `contracts/fixtures/` passes or fails as its name
   says. This suite is run from Go in phase 2 too — one set of files, two validators.
2. **Auth.** Missing token → 401. Wrong token → 401. Revoked token → 401 with no job row. Correct
   token writes a job with the matching `source`. Revoking one source leaves the others working.
   Constant-time comparison (assert via code review + a unit test on the helper's behaviour, not
   timing). **Scope matrix:** generate every (route × token) pair from design §4's route table; a
   door token on any `potato` route is 403 with zero D1/R2 writes, and the `potato` token on `/save`
   or `/upload` is 403 (it is not a door). A route that forgets to declare a scope fails a test that
   walks the router.
3. **`POST /save`.** Returns 200 in <100 ms with only D1 written. Empty body → 400. A 1 MB note is
   capped. A `link` that is not http(s) is rejected. Unknown fields are ignored (forward
   compatibility), unknown `source` is rejected.
4. **`POST /upload`.** R2 object exists *before* the D1 row (assert order via a fake binding that
   records calls). Oversize is rejected by `Content-Length` when present. Disallowed content type is
   rejected. A body that dies halfway leaves no job row (and an orphan the sweep will take).
5. **Claim.** One job, two claims → the second gets nothing. `claim?limit=5` over 12 jobs returns 5,
   then 5, then 2, with no overlap. Claim is atomic under concurrency (S2 harness, kept as a test).
   `done` → `status=done`, on a second `done` is idempotent. `failed` stores `last_error` and
   increments `attempts`. **Pending** returns queued + claimed jobs and changes nothing (assert no
   row is modified). **Album hold:** a job with a `group_key` touched 3 s ago is not claimable, an
   ordinary job created 1 s ago is, and a claim or lease reclaim never moves `touched_at`.
   **Replay:** after ten jobs are marked done, `replay?since=` returns exactly those ten with full
   payload and blobs.
6. **Lease.** A claim older than the lease is reclaimable; a claim inside the lease is not; a job
   that exhausts attempts becomes `failed` and stops being served.
7. **Sweep.** Orphaned `incoming/` objects older than 7 days are deleted; objects with a live job row
   are not; a *job* whose blob is missing is marked `failed`, not deleted.
8. **Presign.** `potato` scope only; only keys that exist in `blobs` or under the allowed prefixes;
   GET only; TTL ≤ 300 s; the signed URL actually fetches from R2's S3 endpoint (an integration check
   against the S1 test bucket, since unit tests cannot prove an AWS-style signature is accepted).
9. **Retention.** No cron or route ever deletes a `done` job or its `incoming/` blob (a test runs both
   crons against a backdated done job and asserts it, its blobs and its R2 object survive).

### Fuzz / property targets

- `fast-check` on `validate.ts`: any JSON object either validates or produces a typed error; never
  throws into a 500; the size/type caps hold for generated `Content-Type` and `Content-Length`.
- `fast-check` on claim: given N concurrent claims over M jobs, total claims ≤ M and no job is claimed
  twice at the same time (the S2 harness as a property test).
- Go side (phase 2): `FuzzJobEnvelope` reads the same fixtures.

## Tasks

- [ ] `wrangler.toml` with D1 + R2 + cron; `.dev.vars` for local tokens; document secrets in the repo
      README, never commit them.
- [ ] Migration 0001 (jobs, blobs, tokens) and a `migrations/README` explaining that D1 migrations
      are applied by `wrangler d1 migrations apply`.
- [ ] Token bootstrap: a small script or documented `wrangler d1 execute` to insert a hashed token
      for each door. No token ever printed after creation.
- [ ] `/save` with caps: note ≤ 64 KB, link length ≤ 2 KB, JSON body ≤ 96 KB.
- [ ] `/upload`: raw body only, streaming `BUCKET.put(key, request.body)`, `incoming/<job>/<n>`,
      `Content-Length` required (411 otherwise, per S1), content-type allowlist (image/*,
      application/pdf, text/plain, text/markdown), 25 MB, metadata in headers/query.
- [ ] `/jobs/claim?limit=N&kinds=link,note,upload` (`kinds` defaults to all; the potato asks only for
      kinds it can process, so before phase 6 an uploaded photo waits safely in the queue instead of
      burning attempts), `/jobs/pending`, `/jobs/replay`, `/jobs/:id/done`, `/jobs/:id/failed`,
      all `potato` scope (the potato holds one credential, a distinct row so it can be rotated
      alone; door tokens never reach these routes).
- [ ] Cron: lease reclaim (every 5 min), orphan sweep (daily), 7-day window.
- [ ] `/presign` (`potato` scope) returning `{url, expires_at}` for a known `r2_key`, signed S3-style
      with `aws4fetch`. Create the read-only R2 API credential scoped to this bucket and store it as
      a Worker secret; record its exact scope in `security-notes.md`.
- [ ] `/healthz` returning `{status, version}` with no D1 read.
- [ ] Rate limiting per IP and per token; 429 with `Retry-After`.
- [ ] Fill in `contracts/`; add the Go validator in phase 2.
- [ ] Deploy: a second Cloudflare account-scoped Worker, a custom hostname or `workers.dev`, recorded
      in `security-notes.md` as the second allowed egress host.
- [ ] Verify design §4's route table and §11's sweep details match what shipped; the table is the
      source of truth, so fix the code or fix the table in the same commit.

## Exit criteria

Automated: `worker` job green including the S2 property test; contract fixtures enforced in CI.

Demo, with `curl` and nothing else behind it:

1. `curl -X POST /save -H "Authorization: Bearer $SHORTCUT" -d '{"link":"https://example.com/x"}'`
   → 200; `wrangler d1 execute … "select * from jobs"` shows one queued row with `source=shortcut`.
2. Upload a 20 MB PNG with `curl --data-binary` → 200; the object is in R2 under `incoming/`; a 26 MB
   file → 413.
3. Two concurrent claim loops process 200 jobs with zero double-claims (S2 verdict recorded with
   numbers).
4. Kill a claim loop mid-job; after the lease the job is claimable again, `attempts` incremented.
5. Revoke the `shortcut` token; its saves 401 while `telegram` and the potato credential still
   work; a `shortcut` token on `/jobs/claim` and on `/presign` is 403.
6. Sleeping 7 days is simulated by backdating `created_at` on an orphan: the cron removes it and
   leaves live keys alone.
7. Mark ten jobs done, backdate them 30 days, run both crons: all ten, their blobs and R2 objects
   remain, and `GET /jobs/replay?since=` returns them.

## Risks

- **D1 free-tier write limits.** Every save is a write. At 100/month this is nothing, but the sweep
  must not rewrite rows it doesn't need to; batch the lease reclaim.
- **`Content-Length` and chunked bodies.** A client can lie about `Content-Length`; the runtime
  enforces the declared length, so a lie fails the upload instead of bypassing the cap. Whether R2
  accepts a chunked body at all is S1's question; if it does, enforce the cap mid-stream by
  aborting and state honestly that the guard is best-effort.
- **Duplicate delivery is legal.** The potato must be idempotent by canonical URL and sha256, so the
  Worker is allowed at-least-once. Do not build exactly-once semantics the DB cannot support.
- **Two secrets in two places.** The Worker's token table and the potato's `.env` must be rotated
  together; write the rotation drill in `security-notes.md` now.

## Build-time decisions to record

- Presign is **settled**: `potato` scope only, S3-style, signed with `aws4fetch` and a read-only R2
  credential held as a Worker secret. A Worker-served HMAC URL was rejected because it would be a
  public read path through the Worker. Record the credential's exact scope.
- Whether the Worker validates the URL's scheme/host before enqueuing (lean: scheme only; the potato
  canonicalises and rejects).
