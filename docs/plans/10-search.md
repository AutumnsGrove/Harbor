# Phase 10 — Hybrid search and related items

**Status:** planned · **Design refs:** [§8](../design.md#8-search),
[C3](../open-questions.md) · **Depends on:** phases 3, 9 (chips/filters) · **Gate:**
[S3](spikes.md#s3--brute-force-vector-cache-at-10k-items),
[S5](spikes.md#s5--nova-embed-returns-what-designmd-says) · **Est:** 1 evening + tuning

## Goal

"That article about the thing" works. Keyword and semantic results fuse into one ranked list, update
as you type, and filter by kind/tag/source/date. Related items on the item page come from the same
index. Memory stays flat because the vector cache is a plain float32 slice, loaded once and appended
by the indexer — not `sqlite-vec`, not an ANN library.

This is the last phase where a design assumption (brute force is enough) is settled by measurement
rather than argument. S3 provides the number; this phase makes it true in the product.

## Scope

In: FTS5 query builder (from phase 3) extended with filters, an in-memory vector cache with a rebuild
path, cosine top-K, reciprocal rank fusion, the instant-search UI with chips, related items, ranking
weights, a benchmark at 10k synthetic items.

Out: ANN indexes, visual/brand search (mymind's "Same Vibe"), query expansion, learned ranking, search
history, saved searches beyond Spaces.

## Deliverables

```
server/internal/search/
  fts.go         query build: escape, prefix matching, filters, snippets, limits
  vector.go      cache: load at boot, append on index, rebuild task, normalise on write
  cosine.go      top-K over []float32 (heap, no full sort), parallel chunks above a size
  rrf.go         reciprocal rank fusion, weights, deterministic tie-break
  related.go     top-N excluding self, threshold, per-item cache
  search_test.go, bench_test.go
server/internal/httpapi/search.go   GET /api/search?q=&kinds=&tags=&sources=&after=&before=&cursor=
web/src/lib/search/store.ts         debounced query, abort on change, URL sync
web/src/routes/+page.svelte         chips + instant results + masonry + pending
```

## Mechanics

- **Vector cache**: `map[itemID][]float32` (or a flat `[]float32` + index map). Loaded at boot from
  `item_embeddings` in one pass; appended on write; rebuilt on a model/dims change. A rebuild is a full
  read + normalise, and it is a task, not a startup requirement.
- **Normalise on write** so query time is a dot product, not a division per candidate. Store the
  original vector in SQLite; the cache holds the normalised copy. `S5` proved they are unnormalised.
- **Cosine top-K** with a bounded min-heap of size 50; skip zero vectors; over ~10k items this is
  sub-50 ms single-threaded per the S3 numbers. Chunked parallel fan-out only if S3 says it is needed —
  not by default, because goroutines on a two-core potato are not free.
- **RRF** with `k=60` (the standard), weights `w_fts`, `w_vec` from config, deterministic tie-break on
  `items.id` so the same query always returns the same order. Weights are the one thing left to tune
  against real data; log the top result for a handful of queries during tuning.
- **Filters apply to both lists before fusion**, so a filtered semantic search doesn't rank items the
  filter excludes and then drop them (that produces short, weird result pages).
- **Snippet** from FTS5 `snippet()`; for semantic-only hits, fall back to the summary with the matched
  terms highlighted client-side (simple, no server work).
- **Latency budget**: 200 ms debounce, 2-char minimum, target p95 <150 ms end-to-end at 10k items,
  aborting in-flight requests on keystroke.

## Tests first

1. FTS: prefix matching ("archi" finds "architecture"), phrase quoting, escaping, filters, and
   `snippet()` returning no raw HTML tags.
2. Cosine: known vectors → known nearest neighbour; equal vectors → 1.0; orthogonal → 0; dims mismatch
   → error, never a silent truncation; empty cache → empty result, no panic.
3. Top-K correctness vs a brute-force full sort on random data (`rapid`): same set, same order within
   ties.
4. RRF: a document in both lists outranks one in only the higher-ranked list when weights are equal;
   changing `w_vec` changes order as expected; identical inputs are deterministic across 100 runs.
5. Filters: a kind filter applies to both lists (a semantic-only hit of the wrong kind never appears);
   a tag filter uses the canonical tag id (alias resolves); date bounds inclusive/exclusive per the
   Space semantics.
6. **Degraded mode**: with zero embeddings (a fresh import, embedding disabled, or a Nova outage),
   search still returns FTS results and the UI says nothing alarming — a test asserting the endpoint
   never 500s on an empty cache.
7. Related: never returns the item itself; respects the threshold; returns 3–6 (fewer when the library
   is small); a re-embed changes the set.
8. Cache lifecycle: boot load, append, delete, rebuild; a rebuild during a query doesn't return a
   half-built list (atomic swap).
9. `BenchmarkSearch` at 10k: record ns/op and allocs. `BenchmarkSearchVectorOnly` too.
10. Latency budget test with a fake clock is not meaningful — instead assert the handler does no work
    beyond one FTS query + one cache scan + fusion (no per-result DB queries; assert with a query
    counter on the store).

## Tasks

- [ ] Implement S3's chosen variant exactly, and put its measured numbers in a comment above the cache
      so nobody "optimises" it back.
- [ ] Boot: load vectors; if the count mismatches `item_embeddings`, rebuild and log once.
- [ ] Indexer: append on write, remove on delete/purge, update on re-embed (content hash).
- [ ] `GET /api/search` with cursor pagination over the fused list; total count is not required (it is
      expensive and unused) — return `has_more`.
- [ ] Instant search UI: debounce 200 ms, abort on change, URL reflects `?q=`/filters (shareable), no
      separate search page, `/` focuses the box, `Esc` clears.
- [ ] Filter chips: kind, tag, source, date — combined, with a visible "clear filters".
- [ ] Related items row on the item page: 3–6, quiet, click-through, no graph view.
- [ ] Ranking tuning session: 20 real queries, log the top 10 for each, adjust weights once, record the
      final weights and the reasoning in design.md §8.
- [ ] A `make bench-search` target that builds a synthetic 10k DB, so the number is reproducible on the
      potato and not just on the Mac.
- [ ] Update design.md §8 with the measured numbers and the final weights; resolve the "still open"
      item in §13.

## Exit criteria

Automated: cosine/RRF/filter/degraded tests green; `rapid` top-K equivalence green; benchmarks recorded;
`make check` green.

Demo:

1. Type 3 characters → results in under ~150 ms, filling in as you type, no flicker, no full page load.
2. Query a concept whose words are not in the title ("the piece about the woman who mapped the ocean")
   → the right item ranks top-3.
3. Filter to `kind=recipe` + a tag + last 90 days → results are consistent, counts sane, clearing
   filters restores.
4. Remove the embedding for one item → keyword search still finds it, semantic search doesn't, and
   nothing errors.
5. Open an item → 3–6 related items that a human agrees are related; note any absurd ones.
6. Kill the vector cache (restart with a rebuild forced) → search still works during the rebuild.
7. On the potato: `docker stats` during a search burst; RSS and latency recorded. 10k synthetic
   items: p95 latency and peak RSS in the exit note.

## Risks

- **Brute force is fine until it isn't.** S3 sets the checkpoint; write it down (e.g. "revisit at 50k")
  so the future decision is a measurement, not a mood.
- **Tuning weights against 20 queries overfits.** Say so; keep the weights in config and revisit with
  real usage rather than locking them into code.
- **A stale cache is worse than a slow one.** Every write path must go through one `search.Index`
  method; a test asserts item count == cache size after a mixed workload.
- **Memory on the potato.** 10k × 1024 × 4 = 40 MB, plus the normalised copy if you keep both — don't.
  One copy, normalised, plus SQLite as the source of truth.
- **Debounce + abort races** cause out-of-order results in the UI. Assert last-write-wins on the client
  with a test.
- **Fusion hides failures.** If the vector path throws, don't silently fall back without logging; a
  counter on the Status page ("vector: ok/degraded") makes it visible.

## Build-time decisions to record

- Cache representation (map vs flat slice) and the measured difference.
- RRF `k` and the final weights.
- Whether related items are computed live per page view (cheap: 10k × 1024 dot products) or cached per
  item — recommend live, and measure.
- The checkpoint count for revisiting brute force.
