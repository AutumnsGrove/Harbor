# Phase 9 — Tags and Smart Spaces: browsing by theme

**Status:** planned · **Design refs:** [§9](../design.md#9-ui-sveltekit-embedded),
[H3](../open-questions.md), [J5](../open-questions.md), [J3](../open-questions.md),
[I4](../open-questions.md) · **Depends on:** phase 3 · **Gate:** none · **Est:** a weekend

## Goal

The library stops being a list and becomes browsable by meaning: canonical tags with a real manager
(rename, merge, alias), Smart Spaces as live rules, saved queries, hand-curated collections (Read
later and imported Raindrop collections), bulk actions for cleanup, and Recently deleted with restore.

The rule evaluator is the interesting engineering. Its vocabulary is the one in
[open-questions H3](../open-questions.md) (below), which the mockup's `ruleTest` approximates: the
AND/OR *semantics* match the mockup, but its field names and its tag-overlap `similar_to` are mockup
shortcuts that change when the UI is ported (open-questions N7). The reference evaluator in the tests
is written from this vocabulary, not copied from the mockup's JavaScript.

## Scope

In: tag manager UI (rename, merge, delete, aliases — the normalise/resolve write path itself is phase
3's), Space rules (parse/evaluate), auto/saved/manual
Spaces, Space chips on home, Space editor with live preview and counts, AI Space suggestions (suggest
only), Read later as a manual Space, bulk select + 4 actions, Recently deleted with restore and delete
now.

Out: nested Spaces (folder-like UI), shared Spaces, Space-specific sorting beyond the global one,
hierarchy in tags.

## Deliverables

```
server/internal/spaces/
  rule.go        parse + validate {tags_all, tags_any, kinds, sources, after, before, text, similar_to_item}
  eval.go        rule → SQL WHERE + args (never string-concatenated); `similar_to_item` resolves to a
                 candidate id list first (see Rules)
  suggest.go     pattern detection over tags/kinds → suggestion prompt (no auto-create)
  rule_fuzz_test.go
server/internal/store/
  tags.go        merge (transactional), rename, delete, counts — normalise/resolve already exist (phase 3)
  spaces.go      CRUD, membership, counts, chips query
  bulk.go        delete/tag/add-to-space/refresh as one transaction per batch
  purge.go       the 30-day purge job
server/internal/httpapi/
  tags.go spaces.go bulk.go deleted.go
web/src/routes/spaces/+page.svelte, spaces/[id]/+page.svelte, spaces/new/+page.svelte
web/src/lib/spaces/RuleEditor.svelte, rulePreview.ts
web/src/routes/settings/tags/+page.svelte, settings/deleted/+page.svelte
web/src/lib/components/BulkBar.svelte, SpaceChips.svelte
```

## Rules (the exact vocabulary)

```ts
type Rule = {
  tags_all?: string[]      // AND
  tags_any?: string[]      // OR
  kinds?: Kind[]           // OR
  sources?: Source[]       // OR
  within_days?: number     // saved_at >= now - N days, evaluated at read time (the relative window)
  after?: string           // ISO date, saved_at >= (absolute; for saved queries)
  before?: string          // ISO date, saved_at <  (absolute, exclusive)
  text?: string            // FTS over title/summary/content/note/tags
  similar_to_item?: string // cosine to that item's embedding >= a fixed config threshold
}
```

Fields AND together; lists OR within a field. `within_days` exists because an auto Space such as
"Long reads from the past 90 days" must not freeze at its creation date — a stored absolute `after`
would be stale tomorrow. Dates compare against `saved_at` (the home grid's clock).

**Evaluation is SQL, built from typed fragments.** `text` goes through the same escaped FTS builder
as search (and matches tag names via `tags_text`). No rule field may ever be interpolated into a
string. `similar_to_item` cannot be a `WHERE` clause on its own: it is resolved first, through a
`Similarity` interface, to a list of candidate item ids that is then bound as parameters.

**`Similarity` before phase 10.** Phase 10 (the vector cache) comes after this phase in the build
sequence, so phase 9 ships a simple implementation of the interface that scans `item_embeddings`
(embeddings exist since phase 3) and computes cosine in Go — fine at this scale, tested against the
same fixtures — and phase 10 swaps in the cache-backed one without touching the rule code. The
threshold is config, not UI, so a Space is stable rather than drifting with a slider.

**Mockup deltas to carry into the UI port:** `within` → `within_days`; `similar_to` →
`similar_to_item` (real cosine, not shared-tag); `text` also searches content and tags; dates compare
`saved_at`.

## Tests first

1. **Rule semantics as a table**, one case per operator and per combination: `tags_all` with two tags
   requires both; `tags_any` with two requires either; `kinds + tags_any` ANDs across fields; empty rule
   matches everything; an unknown field is rejected at write time (not ignored); a date with a timezone
   is normalised; `before` is exclusive; `within_days: 90` is evaluated against *now* at read time (a
   Space is not frozen at creation); `text` also matches tag names; the unknown-field rejection
   covers the old mockup names (`within`, `similar_to`) so a stale client fails loudly.
2. `FuzzSpaceRule`: arbitrary JSON never panics, terminates, rejects unknown operators, and produces
   SQL with exactly one placeholder per supplied value (assert the arg count matches — this is the
   injection guard).
3. `rapid`: equivalence between the SQL evaluator and a pure in-memory reference evaluator over
   generated items and rules. If they disagree, one is wrong — this is the strongest test in the phase and it is cheap. The reference evaluator is written from
   the vocabulary above, not copied from the mockup's JavaScript (the mockup's `similar_to` is a
   tag-overlap shortcut).
4. **Tag normalisation** is covered in phase 3 (test 20). Here, assert only that the manager's
   rename and merge call that same function and refresh `tags_text` and the FTS row for every
   affected item.
5. **Merge leaves an alias**: merge `js` into `javascript` → items reassigned, `tag_aliases['js'] =
   javascript.id`, and the next write of `js` resolves to `javascript`. Merge is transactional: a
   failure mid-way leaves nothing half-moved, and every affected item's `tags_text` and FTS row are
   refreshed in the same transaction. `rapid`: merging is idempotent and alias chains never
   cycle (resolve recursively with a depth cap).
6. **Delete a tag** removes `item_tags` rows but not items; the tag can be recreated; its aliases are
   removed.
7. **Bulk actions are atomic per batch**: 200 items, one failure → nothing changed (or a documented
   partial-with-report — **decide: all-or-nothing in one transaction**, because the UI shows one
   result).
8. **Bulk refresh** enqueues jobs, does not block, and is de-duplicated against in-flight jobs for the
   same item.
9. **Recently deleted**: list returns soft-deleted within 30 days; restore returns it to the library
   with tags and highlights intact; "delete now" purges DB + local media + R2 keys (the Worker's `DELETE /objects` is
   faked on the potato side; the route itself is tested in the Worker suite, including that a door
   token is refused); the purge job removes items older than 30 days and nothing else. Restore of an item whose
   canonical URL now exists in a live duplicate → **decide: merge on restore**, with the note appended.
10. **Read later** is a manual Space: membership is `space_items`, deleting the Space deletes no items,
    and the item page toggle edits membership.
11. **Suggestions**: a suggestion never creates a Space; accepting it creates one with the suggested
    rule and the user's name; dismissing it is stored in `space_suggestions` by rule fingerprint, so it does not reappear nightly.

## Tasks

- [ ] `spaces/rule.go` + `eval.go` before any UI, with the reference evaluator in `_test.go`.
- [ ] Write the in-memory reference evaluator in the test file from the vocabulary above so drift
      between it and the SQL is caught by the equivalence test; record the mockup deltas listed in
      Rules for the UI port.
- [ ] `Similarity` interface with a scan-based implementation over `item_embeddings` (phase 10 swaps
      in the cache-backed one).
- [ ] Manual tag edits and the tag manager call the phase 3 tag write path; there is exactly one
      implementation of normalise → resolve alias → reuse canonical → insert.
- [ ] Tag manager: list with counts, rename, merge (searchable target picker), delete with a
      confirmation that says how many items are affected.
- [ ] Spaces: list with counts and a preview thumbnail; create/edit with the rule editor; live preview
      showing "N items" as the rule changes (debounced, server-evaluated — the preview must use the
      real evaluator, not a JS copy).
- [ ] Home chips: horizontal row under search, ordered by usage; "All" first; chips are the primary
      navigation, so they must be keyboard reachable.
- [ ] Bulk select: `x` on desktop, long-press on mobile; bar with Delete, Tag, Add to Space, Refresh;
      counts; escape clears.
- [ ] Recently deleted in Settings with restore / delete now, and a countdown of days remaining.
- [ ] Worker `DELETE /objects` (potato scope; scope-matrix test; deletes only keys under `incoming/`,
      `media/`, `archives/`). Purge job registered in the in-process scheduler: daily, 30 days, logs
      counts, calls that route for R2 keys, removes local media files and R2 keys that no remaining
      row references (reference-count by sha and by key before any unlink or delete).
- [ ] AI suggestions: a nightly job that looks for ≥5 items sharing a new tag or kind in 30 days and
      writes a *suggestion row*; UI shows it in the Spaces list as "suggested"; accept/dismiss.
- [ ] Update design.md §13 with the `similar_to_item` threshold and the restore-merge rule.

## Exit criteria

Automated: rule table + fuzz + the equivalence property green; merge/alias property green; bulk
atomicity test green; purge tests green; `make check` green.

Demo:

1. Imported library open on Home: chips show the imported Raindrop collections; clicking one shows
   exactly its items.
2. Create an auto Space "Long reads" = `{kinds:[article,paper], within_days: 90}`; the count is right,
   updates as new items land, and is still right when a day passes (the window moves).
3. Create `{tags_any:["rust","go"], tags_all:["systems"]}` and verify against a hand-checked list.
4. Rename `webdev` → `web development`; merge `js` into `javascript`; re-run the Raindrop import;
   confirm `js` items land on `javascript` and the old spelling does not return.
5. Select 50 items, tag them, add them to a Space, delete 5, restore 3 — all outcomes correct and the
   counts reconcile.
6. Delete an item, restore it a week later (backdate `deleted_at`), confirm tags/highlights/media are
   intact.
7. Let the purge run on a backdated item → row gone, local file gone (if unreferenced), R2 key
   deleted, item count correct.
8. Accept a suggested Space; dismiss another; the dismissed one stays gone after a restart.

## Risks

- **Rule/SQL divergence.** The equivalence property is the only thing standing between a Space and a
  silently wrong result set. Keep the reference evaluator.
- **Tag merge is destructive.** It touches every item; run it in one transaction, snapshot first on a
  real library, and show the affected count in the confirmation.
- **Bulk refresh at 200 items** creates 200 enrichment jobs and a bill. Cap the batch (e.g. 50) and say
  so in the UI, or make refresh explicitly staged.
- **Purge deleting a file still referenced** by another item's `media` row (content addressing means
  sharing). Reference-count before unlink — and before deleting the matching R2 key; test the shared-file case
  explicitly for both.
- **Chips as primary navigation** means their ordering and counts matter. Sort by item count, cache the
  counts, and never leave a chip showing a stale count after a bulk action outside the Space.

## Build-time decisions to record

- The `similar_to_item` cosine threshold and whether it is exposed (recommend: config only).
- Restore-merge vs restore-conflict behaviour.
- Whether Space counts are computed on read (cheap at this scale) or cached (invalidate on write) —
  recommend read, with a measurement at 10k items.
- Bulk batch cap for Refresh.
