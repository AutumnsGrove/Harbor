# worker

Harbor's public front door: a write-only queue on Cloudflare. It holds a D1 job
queue, an R2 intake bucket and hashed door tokens. It never holds the library and
has no public read path — the potato pulls, and only the potato-scope credential
can claim, read or close a job.

The route table, the scopes and the reasons are in
[design.md §4](../docs/design.md#worker-api-and-token-scopes-settled-2026-11-04);
the build plan and its tests are in [01-worker.md](../docs/plans/01-worker.md).

```bash
pnpm install
pnpm typecheck          # tsc --noEmit
pnpm test               # vitest in workerd with real D1 + R2 (miniflare)
pnpm migrate:local      # apply migrations to the local dev database
pnpm deploy             # wrangler deploy
```

## First deploy

```bash
wrangler d1 create harbor                    # once; put the id in wrangler.toml
wrangler r2 bucket create harbor-intake      # once; private
wrangler d1 migrations apply harbor --remote

# door tokens: run this per door, keep the printed token, apply the printed SQL
node scripts/mint-token.mjs shortcut door
node scripts/mint-token.mjs telegram door
node scripts/mint-token.mjs potato potato

# /presign needs a read-only R2 API token scoped to this bucket (dashboard →
# R2 → Manage API tokens). Until it is set, /presign answers 503 and everything
# else works; no read path is invented as a fallback.
wrangler secret put R2_ACCOUNT_ID
wrangler secret put R2_ACCESS_KEY_ID
wrangler secret put R2_SECRET_ACCESS_KEY

wrangler deploy --var VERSION:r$(git rev-list --count HEAD).$(git rev-parse --short HEAD)
```

## What is deliberately not here

- No route reads an item, because there is no item: the library is SQLite on the
  potato. `/jobs/*` and `/presign` are `potato`-scope, and a door token on any of
  them is a 403 with no side effects (there is a test that walks the router).
- No Telegram webhook, `/mirror`, `/objects` purge, `/render` or `/backups`:
  those arrive with phases 4, 6, 9, 11 and 12 and are listed in the plan.
- No plaintext token anywhere: `tokens.sha256` is compared in constant time, and
  revoking one door leaves the others working.

## Caps

`POST /save` ≤ 96 KB, note ≤ 64 KB, link ≤ 2 KB and http(s) only.
`POST /upload` ≤ 25 MB, content-type allowlist, raw body only. The limit is
checked from the declared `Content-Length` (the edge always provides one — spike
S1) with a counting stream as defence in depth.
