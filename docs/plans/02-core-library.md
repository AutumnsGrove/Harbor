# Phase 2 — The core library: schema, poll loop, composer

**Status:** planned · **Design refs:** [§4](../design.md#4-architecture),
[§5](../design.md#5-save-path), [§7](../design.md#7-data-model-sketch--open-in-details),
[§11](../design.md#11-operations)
**Depends on:** phase 1 · **Gate:** none · **Est:** 1 evening + part of a weekend

## Goal

The potato becomes the library. This phase lands the whole schema (including tables whose UI arrives
later, because migrating them in is cheaper than bolting them on), the outbound poll loop with a
lease, URL canonicalisation and merge-on-resave, and the in-app composer. At the end of it, a link
saved from `curl` is a real item you can open and read, and a note you type in the app is in the same
list.

Everything is deliberately unenriched: `kind` defaults to `article`, tags are empty, search is FTS
over whatever text exists (title + note). That is a working link-and-note manager, which is the floor
the rest of the project stands on.

## Scope

In: `store` (open, migrate, all queries), `model`, `canon`, `queue` (poll/claim/lease/retry),
`workerclient` (contract-validated), item/merge semantics, `POST /api/items`, the pending window, the
home grid (masonry), the item page skeleton, the composer, soft delete plumbing.

Out: fetching pages, AI, media, archives, Spaces UI, search ranking. Tables for tags, spaces, media,
highlights, and embeddings are created now but only used from phase 6+.

## Deliverables

```
server/internal/store/
  db.go            open, WAL, pragmas, integrity check on boot
  migrate.go       embedded migrations, forward-only, schema_version
  migrations/0001_init.sql   the full schema from design.md §7, all tables
  items.go         insert/merge/get/list/update/soft-delete/purge
  tags.go          (created, unused) minimal read path
  jobs.go          local mirror of processed job ids for idempotence (job_id, done_at)
server/internal/model/    Item, Media, Tag, Space, Highlight, enums (Kind, State, Source)
server/internal/canon/    CanonicalURL (fuzzed, property-tested)
server/internal/queue/    Loop, Claimer, Backoff, Reclaimer
server/internal/workerclient/  typed client + contract validation
server/internal/httpapi/  items.go, save.go, search_stub.go
web/src/routes/           / (home), /item/[id], /settings (stub)
web/src/lib/components/   Card, MasonryGrid, Composer, PendingStrip, Tag, KindBadge
```

## Data model notes for this phase

The DDL in design.md §7 is the baseline. Three things to settle while writing `0001`:

- **`rowid` and `items.id`.** FTS5 `content='items'` needs `content_rowid`. If `items.id` is a TEXT
  primary key, either rely on the implicit `rowid` (fine, but it must be stable and never reused —
  SQLite reuses rowids after vacuum unless `AUTOINCREMENT`) or add an explicit `INTEGER PRIMARY KEY`
  surrogate and make `id` a unique TEXT. **Decide: add a surrogate `rowid` column with
  `AUTOINCREMENT` is not allowed on a non-PK**, so the clean option is `items(seq INTEGER PRIMARY KEY
  AUTOINCREMENT, id TEXT UNIQUE NOT NULL, …)` and point FTS at `seq`. Record this in design.md §7 —
  it is exactly the kind of detail that silently breaks reindexing later.
- **Triggers vs explicit writes** for `items_fts`. Explicit writes in the same transaction as the item
  are easier to reason about and easier to test than triggers, and the code path is already
  centralised in `store`. **Decide: explicit, with a `store` test that asserts item/note/title edits
  update FTS.**
- **`deleted_at` vs a separate table.** Keep the column (design.md §7). Every list query filters
  `deleted_at IS NULL`; that filter lives in one helper, not repeated 14 times.

Schema-version bookkeeping: `PRAGMA user_version` plus a `schema_migrations` table (the pragma for the
fast check, the table for history). Forward-only: a migration that needs a backfill runs in a
transaction and takes a `VACUUM INTO` snapshot first when the DB is non-empty.

## Tests first

**Pure rules**

1. `canon.CanonicalURL` — table tests for lowercase host, strip fragment, strip `utm_*`/`fbclid`, keep
   meaningful query, drop trailing slash policy, unicode/punycode hosts, invalid schemes, no scheme,
   credentials in the URL. Then:
   - `FuzzCanonicalURL`: never panics, output re-canonicalises to itself, scheme ∈ {http, https}.
   - `rapid`: idempotence and "two spellings of the same page → one key" over generated variations
     (case, tracking params, fragment, default port, `www.` policy — **pick one: keep `www.` as
     distinct**, because collapsing it merges genuinely different sites).
2. Merge (`store.MergeItem`) — append note with a timestamp separator, union tags, keep Read later
   membership, refresh metadata, bump `updated_at`, never duplicate. `rapid`: `merge(merge(a,b),c)`
   note order is stable; merging the same note twice… **decide**: appending the identical note twice
   is a no-op (compare trimmed text) — record it.
3. Retry/backoff — `queue.Backoff(attempt)` returns increasing durations, never exceeds the cap, and
   after `attempts=4` the job is terminal. Table test.

**Store**

4. Migration from empty → expected tables, indexes, FTS shadow tables; opening twice is a no-op; an
   old `user_version` upgrades and preserves rows.
5. `items.url` partial unique index rejects a duplicate canonical URL but allows many NULL-URL notes.
6. Soft delete hides from list/search, is retrievable by id, and purge cascades (media, highlights,
   tags) and only touches the target item.
7. FTS: insert/update/delete reflect in `items_fts`; a note-only item is findable by its note text.

**Queue**

8. Fake Worker: claim → process → done. A `done` that 500s is retried without reprocessing the item
   (idempotence via `store.jobs` mirror). A 401 pauses the loop and surfaces an error state rather
   than spinning. Network failure backs off exponentially with jitter; a success resets the backoff.
9. Crash test: start the loop, kill it mid-job (context cancel), start again → the job is reclaimed,
   and the item is not duplicated.
10. `rapid` model-based test: a sequence of `enqueue/claim/complete/expire` over a generated trace
    ends with every job either done exactly once or failed exactly once.

**Contract**

11. Go decodes every `contracts/fixtures/*.json` with the same pass/fail expectations as the Worker's
    vitest suite. `FuzzJobEnvelope` on the same shape.

**HTTP**

12. `POST /api/items` with a URL writes an item, returns it, and enqueues enrichment locally (bypassing
    the Worker, per design.md §5). Duplicate URL → merge response with a flag, not a second row.
13. `GET /api/items` returns live items newest-first with a cursor, excludes deleted, and its latency
    on 10k rows is <50 ms (assert <150 ms to leave CI headroom).

## Tasks

- [ ] `store` open: WAL, `busy_timeout`, `foreign_keys=ON`, `synchronous=NORMAL`, integrity check on
      start (log, don't fail).
- [ ] Write `0001_init.sql` complete — items (with `seq`), tags, tag_aliases, item_tags, media,
      highlights, spaces, space_items, archives, items_fts, item_embeddings, jobs mirror.
- [ ] `store` query layer with named functions; no SQL outside the package.
- [ ] `canon` + `merge` with tests green before any handler exists.
- [ ] `workerclient` with typed structs, `contracts/` validation, timeouts, and a `User-Agent` that
      identifies the version.
- [ ] `queue.Loop`: 30 s cadence with jitter, immediate poll trigger channel (used by the UI), one job
      at a time, context-aware shutdown, structured log line per job.
- [ ] Pending window: the poll response's queued/claimed jobs render as greyed cards at the top of the
      home grid, with the source icon. Clear them on the next poll when they become items.
- [ ] `POST /api/items` (in-app save) writing locally, then handing the item to the enrichment queue.
- [ ] Home: masonry grid ported from the mockup (1 / 2–4 columns), card component with cover
      placeholder, kind badge, tags (empty for now), hover details on desktop.
- [ ] Item page: media block (empty), title, note (plain textarea this phase), tags, source, archive
      link placeholder, related placeholder. Every field editable via `PATCH /api/items/:id`.
- [ ] Composer: URL + Markdown note + up to 10 images (images stored in phase 6 — accept and park them
      in `data/incoming/` with a TODO row so the UI is honest), merge warning on an existing URL.
- [ ] Soft delete + "Recently deleted" read path in Settings (restore lands in phase 9; this phase
      only needs the plumbing to be correct).
- [ ] First-run empty state: Import or paste a first link (Import is a stub until phase 5).

## Exit criteria

Automated: `make check` green; `FuzzCanonicalURL` and `FuzzJobEnvelope` passing their seed corpora and
a 30 s run; the queue model-based test green.

Demo:

1. Save a link by `curl` to the Worker; within 30 s it appears as a pending card, then as an item with
   a title (from `og:title` if fetch exists yet — otherwise the URL) and its source.
2. Save the same link again with a different note from the in-app composer → one item, two notes,
   `updated_at` bumped, merge warning shown before submitting.
3. Type a note with no URL → an item of kind `article` with no URL; it appears in the list; saving the
   same note text twice does not duplicate.
4. Open the item, edit the title and note, reload → persisted.
5. Delete an item → gone from home, present in Recently deleted.
6. Stop the Worker (or block egress) → saves fail with a visible banner, in-app saves still work, and
   the loop backs off without a tight error loop.
7. Kill the container mid-poll; restart; no duplicate items and the in-flight job is eventually done.
8. `PRAGMA integrity_check` ok after all of the above.

## Risks

- **FTS5 + TEXT primary keys** — the `seq`/`rowid` issue above; get it right in `0001` or reindexing
  becomes a data migration.
- **Merge drift.** Merge is the most-touched rule in the product; keep it in one function on the
  server, never duplicated in the UI's "will merge" warning logic (the UI asks the server).
- **Poll loop as a hidden server.** It is a goroutine with a context; give it a health signal
  (`last_poll_at`) that `/api/status` will later read, and make "loop stopped" visible in logs at
  least once per minute.
- **10-image composer before the media pipeline exists.** Do not silently accept and drop. Either
  store them inertly with a clear "not yet processed" state, or cap the composer to 0 images this
  phase. **Decide: accept and store inertly**, so the UI is exercised early.

## Build-time decisions to record

- `www.` policy in canonicalisation (recommend: distinct).
- Duplicate-note handling on merge (recommend: no-op if identical trimmed text).
- Whether `items.kind` for a URL-less note defaults to `note` rather than `article` (design.md §7 says
  the default is `article`; a URL-less note is clearly `note`, and that can be set at write time
  without changing the default).
