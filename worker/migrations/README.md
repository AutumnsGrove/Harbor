# D1 migrations

Forward-only, numbered, applied by wrangler — the Worker never migrates itself.

```bash
wrangler d1 migrations apply harbor --local    # miniflare, what the tests use
wrangler d1 migrations apply harbor --remote   # the real queue
```

`0001_init.sql` creates the whole Worker-side schema: `jobs`, `blobs`, `tokens`.
There is no library schema here and there never will be — the library is SQLite
on the potato (design.md §7).

The test suite applies these to a throwaway database per run (`test/apply-migrations.ts`).
