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

In: Hono app, D1 schema (`jobs`, `tokens`), R2 intake, token hashing/verification, rate limiting,
size/content-type caps, claim/done/failed with lease, `/presign`, `/healthz`, orphan cron,
`wrangler` config, `contracts/` filled in, tests.

Out: Telegram (phase 4), rendering (phase 11), purge (phase 12), anything that reads an item.

## Deliverables

```
worker/
  wrangler.toml              D1 + R2 bindings, compat date, cron trigger
  migrations/0001_init.sql   jobs, tokens
  src/index.ts               routes + middleware
  src/auth.ts                per-source token verify (WebCrypto SHA-256, constant-time compare)
  src/queue.ts               create · claim (lease) · done · failed · reclaim sweep
  src/blobs.ts               streamed put to incoming/<job>/<n>, size/content-type guards
  src/presign.ts             short-lived R2 GET for the UI
  src/limits.ts              rate limit binding wrapper, caps as constants
  src/validate.ts            contracts/*.schema.json validation (hand-rolled or zod)
  test/*.test.ts             vitest + miniflare, real D1/R2
  test/*.prop.ts             fast-check properties
contracts/
  save.schema.json           {link?, note?, title?, source, images[]?}
  upload.schema.json         multipart/raw metadata
  job.schema.json            {id, source, kind, link?, note?, title?, blobs[{key,content_type,bytes}]}
  fixtures/*.json            required/optional/malformed cases used by both languages
```

## Data model (Worker-side only)

```sql
-- jobs: everything the potato pulls. Append-only from the public side.
CREATE TABLE jobs (
  id           TEXT PRIMARY KEY,          -- uuid v4
  source       TEXT NOT NULL,             -- telegram|shortcut|quick|app
  kind         TEXT NOT NULL,             -- link|note|upload
  link         TEXT,                      -- raw, uncanonicalised (the potato owns canonicalisation)
  note         TEXT,
  title        TEXT,
  payload_json TEXT NOT NULL DEFAULT '{}',-- source-specific extras (telegram ids, album key)
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
CREATE INDEX jobs_lease ON jobs(lease_until) WHERE status = 'claimed';

-- blobs are described in a child table so one job can carry an album.
CREATE TABLE blobs (
  job_id       TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  n            INTEGER NOT NULL,
  r2_key       TEXT NOT NULL,
  content_type TEXT NOT NULL,
  bytes        INTEGER NOT NULL,
  PRIMARY KEY (job_id, n)
);

-- tokens: hashed at rest, one row per door so they revoke independently.
CREATE TABLE tokens (
  id         TEXT PRIMARY KEY,            -- telegram|shortcut|quick
  sha256     TEXT NOT NULL,               -- hex of the bearer token
  label      TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  revoked_at INTEGER
);
```

**Settled here, not in design.md yet:** the lease. A potato that dies mid-job must not strand a save,
so `claim` sets `lease_until = now + 600`, and the cron returns expired claims to `queued`
(`attempts` already incremented). After `attempts >= 4`, status becomes `failed` and is *visible* in
the UI rather than silently retried forever. Record this in design.md §5's pending-window section.

## Tests first

1. **Contract fixtures validate.** Every fixture in `contracts/fixtures/` passes or fails as its name
   says. This suite is run from Go in phase 2 too — one set of files, two validators.
2. **Auth.** Missing token → 401. Wrong token → 401. Revoked token → 401 with no job row. Correct
   token writes a job with the matching `source`. Revoking one source leaves the others working.
   Constant-time comparison (assert via code review + a unit test on the helper's behaviour, not
   timing).
3. **`POST /save`.** Returns 200 in <100 ms with only D1 written. Empty body → 400. A 1 MB note is
   capped. A `link` that is not http(s) is rejected. Unknown fields are ignored (forward
   compatibility), unknown `source` is rejected.
4. **`POST /upload`.** R2 object exists *before* the D1 row (assert order via a fake binding that
   records calls). Oversize is rejected by `Content-Length` when present. Disallowed content type is
   rejected. A body that dies halfway leaves no job row (and an orphan the sweep will take).
5. **Claim.** One job, two claims → the second gets nothing. Claim is atomic under concurrency
   (S2 harness, kept as a test). `done` → `status=done`, on a second `done` is idempotent.
   `failed` stores `last_error` and increments `attempts`.
6. **Lease.** A claim older than the lease is reclaimable; a claim inside the lease is not; a job
   that exhausts attempts becomes `failed` and stops being served.
7. **Sweep.** Orphaned `incoming/` objects older than 7 days are deleted; objects with a live job row
   are not; a *job* whose blob is missing is marked `failed`, not deleted.
8. **Presign.** Only known keys, only GET, only for a non-revoked source or the tailnet-only path;
   TTL ≤ 300 s.

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
- [ ] `/upload`: streaming `BUCKET.put(key, request.body)`, `incoming/<job>/<n>`, content-type
      allowlist (image/*, application/pdf, text/plain, text/markdown), 25 MB.
- [ ] `/jobs/claim`, `/jobs/:id/done`, `/jobs/:id/failed`, all token-gated with the same source token
      (the potato holds one; it is a distinct `potato` row so it can be rotated alone).
- [ ] Cron: lease reclaim (every 5 min), orphan sweep (daily), 7-day window.
- [ ] `/presign` returning `{url, expires_at}` for a `r2_key`.
- [ ] `/healthz` returning `{status, version}` with no D1 read.
- [ ] Rate limiting per IP and per token; 429 with `Retry-After`.
- [ ] Fill in `contracts/`; add the Go validator in phase 2.
- [ ] Deploy: a second Cloudflare account-scoped Worker, a custom hostname or `workers.dev`, recorded
      in `security-notes.md` as the second allowed egress host.
- [ ] Update design.md §5 with the lease and attempt cap, and §11 with the sweep details as built.

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
5. Revoke the `quick` token; `quick` saves 401 while `shortcut` still works.
6. Sleeping 7 days is simulated by backdating `created_at` on an orphan: the cron removes it and
   leaves live keys alone.

## Risks

- **D1 free-tier write limits.** Every save is a write. At 100/month this is nothing, but the sweep
  must not rewrite rows it doesn't need to; batch the lease reclaim.
- **`Content-Length` lies.** A chunked body has no length; the cap is enforced mid-stream by aborting.
  Test that path, and keep the 25 MB number as a best-effort guard, stated honestly.
- **Duplicate delivery is legal.** The potato must be idempotent by canonical URL and sha256, so the
  Worker is allowed at-least-once. Do not build exactly-once semantics the DB cannot support.
- **Two secrets in two places.** The Worker's token table and the potato's `.env` must be rotated
  together; write the rotation drill in `security-notes.md` now.

## Build-time decisions to record

- Whether `/presign` is token-gated by a separate tailnet-only credential or is open on the Worker and
  protected by an HMAC of the key plus a short TTL (lean: HMAC, because the browser is on the tailnet
  but the Worker is not).
- Whether the Worker validates the URL's scheme/host before enqueuing (lean: scheme only; the potato
  canonicalises and rejects).
