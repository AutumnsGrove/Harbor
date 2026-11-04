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

In: `store` (open, migrate, all queries), `model`, `canon`, `queue` (the **ingest** stage: poll,
drain, claim, ingest, done — and the shell of the **enrichment** stage, which in this phase just
marks items `ready`), `scheduler`, `workerclient` (contract-validated), item/merge semantics,
`POST /api/items`, `harbor replay`, the pending window, the home grid (masonry), the item page
skeleton, the composer (links and notes), soft delete plumbing, the seeded Read later space.

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
  jobs.go          processed_jobs: Worker job ids already ingested (idempotence for ingest and replay)
server/internal/model/    Item, Media, Tag, Space, Highlight, enums (Kind, State, Source)
server/internal/canon/    CanonicalURL (fuzzed, property-tested)
server/internal/queue/    ingest.go (drain · claim · ingest · done) · enrich.go (priority order, retry,
                          Backoff; the model work arrives in phase 3) · loop health signal
server/internal/scheduler/   the one in-process scheduler; last runs persisted in task_runs
server/cmd/harbor/replay.go  `harbor replay --since <time>`: re-ingest the Worker's done jobs
server/internal/workerclient/  typed client + contract validation
server/internal/httpapi/  items.go, save.go, search_stub.go
web/src/routes/           / (home), /item/[id], /settings (stub)
web/src/lib/components/   Card, MasonryGrid, Composer, PendingStrip, Tag, KindBadge
```

## Data model notes for this phase

The DDL in [design.md §7](../design.md#7-data-model-sketch--open-in-details) is authoritative and was
reconciled on 2026-11-04; `0001` is that DDL, verbatim. The questions this plan used to leave open are
now answered there, and each has a test below:

- **FTS5 and rowids.** `items.seq INTEGER PRIMARY KEY AUTOINCREMENT` is the surrogate, `items.id` is
  `UNIQUE`, and `items_fts` points at `seq`. `AUTOINCREMENT` is what guarantees a rowid is never
  reused, so reindexing never lands on the wrong item.
- **Explicit FTS writes, not triggers**, in the same transaction as the item. Because `items_fts` is
  an *external content* table, an update or delete must first issue the FTS `'delete'` command with
  the **old** column values — so the store reads the old row inside the transaction. Test 7 asserts
  it, including `tags_text`.
- **`deleted_at` stays a column.** Every list query filters `deleted_at IS NULL` through one helper,
  not repeated 14 times. The unique URL index deliberately includes deleted rows.
- **Three clocks:** `created_at` (original), `saved_at` (last save; home sorts on it), `updated_at`
  (last edit). Merge bumps `saved_at`; imports set `saved_at = created_at`.
- **Foreign keys everywhere** (`ON DELETE CASCADE` from `items`), so a purge needs one `DELETE`.
- **`0001` seeds the Read later space** (`spaces.slug = 'read-later'`, kind `manual`), so the
  composer's switch and merge's "keep Read later" have something to point at.

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
   membership, refresh metadata, bump `saved_at` (not `created_at`; `updated_at` only for edits),
   never duplicate. Saving a URL whose item is soft-deleted restores it and merges (design §5). `rapid`: `merge(merge(a,b),c)`
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

8. Fake Worker: claim → ingest → done. The fake records call order and the test asserts `done` is
   sent only **after** the item row has committed (a crash between the two re-claims the job and
   ingest is a no-op via `processed_jobs`). A `done` that 500s is retried without reprocessing the
   item. A 401 pauses the loop and surfaces an error state rather than spinning. Network failure
   backs off exponentially with jitter; a success resets the backoff. The loop **drains**: with 12
   queued jobs it keeps claiming until the queue is empty. The pending list is fetched with
   `GET /jobs/pending`, never by claiming.
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
13. `GET /api/items` returns live items ordered by `saved_at` (newest first) with a cursor, excludes
    deleted, and its latency on 10k rows is <50 ms (assert <150 ms to leave CI headroom).
14. **Replay:** `harbor replay --since` against a fake Worker that serves ten already-ingested jobs
    creates zero new items and leaves the library byte-identical; against a database restored from
    before those jobs, it recreates all ten. `rapid`: replaying any subset, once or twice, in any
    order, converges to the same library.
15. A URL-less note is written with `kind='note'`; a URL item with `kind='article'` until phase 3
    changes it; neither is `unknown`.
16. The enrichment shell orders by `priority DESC, next_attempt_at`; an interactive save (priority 0)
    queued after an import item (priority -1) is processed first.

## Tasks

- [ ] `store` open: WAL, `busy_timeout`, `foreign_keys=ON`, `synchronous=NORMAL`, integrity check on
      start (log, don't fail).
- [ ] Write `0001_init.sql` as exactly design §7 — items (with `seq`, three clocks, queue columns),
      tags, tag_aliases, item_tags, media, highlights, spaces (+ seeded `read-later`), space_items,
      space_suggestions, archives, items_fts, item_embeddings, processed_jobs, import_runs,
      task_runs, schema_migrations — with `PRAGMA foreign_keys=ON` and a cascade test per child.
- [ ] `store` query layer with named functions; no SQL outside the package.
- [ ] `canon` + `merge` with tests green before any handler exists.
- [ ] `workerclient` with typed structs, `contracts/` validation, timeouts, and a `User-Agent` that
      identifies the version.
- [ ] `queue` ingest stage: 30 s cadence with jitter, immediate refresh channel (used by the UI),
      drains the queue in batches, ingests one job at a time, sends `done` only after the item
      commits, context-aware shutdown, structured log line per job. The enrichment stage is a
      separate loop that picks `state='queued'` items by `priority DESC, next_attempt_at`; in this
      phase it only flips them to `ready`.
- [ ] `scheduler` skeleton: a registry of named tasks whose last run is persisted in `task_runs`, so
      a reboot never skips a day (later phases register their tasks into it).
- [ ] `harbor replay --since <time>` using the Worker's replay route.
- [ ] Pending window: `GET /jobs/pending` (queued + claimed, read-only) renders as greyed cards at
      the top of the home grid, with the source icon. They clear on the next refresh when they become
      items.
- [ ] `POST /api/items` (in-app save) writing locally, then handing the item to the enrichment queue.
- [ ] Home: masonry grid ported from the mockup (1 / 2–4 columns), card component with cover
      placeholder, kind badge, tags (empty for now), hover details on desktop.
- [ ] Item page: media block (empty), title, note (plain textarea this phase), tags, source, archive
      link placeholder, related placeholder. Every field editable via `PATCH /api/items/:id`.
- [ ] Composer: URL + Markdown note + Read later switch, merge warning on an existing URL (the UI
      asks the server). The image picker is **hidden until phase 6** (decided below).
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
3. Type a note with no URL → an item of kind `note` with no URL; it appears in the list; saving the
   same note text twice does not duplicate.
4. Open the item, edit the title and note, reload → persisted.
5. Delete an item → gone from home, present in Recently deleted.
6. Stop the Worker (or block egress) → saves fail with a visible banner, in-app saves still work, and
   the loop backs off without a tight error loop.
7. Kill the container mid-poll; restart; no duplicate items and the in-flight job is eventually done.
8. `PRAGMA integrity_check` ok after all of the above.
9. `harbor replay --since 1h` re-ingests the recent done jobs: zero new items, zero changes.

## Risks

- **FTS5 external content** — the `seq` key and the old-values-on-update rule above; get them right
  in `0001` or reindexing becomes a data migration. Test 7 is the guard.
- **Merge drift.** Merge is the most-touched rule in the product; keep it in one function on the
  server, never duplicated in the UI's "will merge" warning logic (the UI asks the server).
- **Poll loop as a hidden server.** It is a goroutine with a context; give it a health signal
  (`last_poll_at`) that `/api/status` will later read, and make "loop stopped" visible in logs at
  least once per minute.
- **Images before the media pipeline exists.** **Decided: hide the image picker until phase 6.**
  The first draft proposed parking images in `data/incoming/` with a TODO row, but `media` requires
  a `sha256` and a `local_path`, so inert parking would need schema that does not exist. Never
  silently accept and drop.

## Build-time decisions to record

- `www.` policy in canonicalisation (recommend: distinct).
- Duplicate-note handling on merge (recommend: no-op if identical trimmed text).
- Settled: a URL-less note is written as `note` at insert time (the column default stays `article`
  for URL items). Phase 3 skips classification for notes.
