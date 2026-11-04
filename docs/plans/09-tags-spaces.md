# Phase 9 — Tags and Smart Spaces: browsing by theme

**Status:** planned · **Design refs:** [§9](../design.md#9-ui-sveltekit-embedded),
[H3](../open-questions.md), [J5](../open-questions.md), [J3](../open-questions.md),
[I4](../open-questions.md) · **Depends on:** phase 3 · **Gate:** none · **Est:** a weekend

## Goal

The library stops being a list and becomes browsable by meaning: canonical tags with a real manager
(rename, merge, alias), Smart Spaces as live rules, saved queries, hand-curated collections (Read
later and imported Raindrop collections), bulk actions for cleanup, and Recently deleted with restore.

The rule evaluator is the interesting engineering. It is ported from the mockup's `ruleTest`, which is
the reference vocabulary, and it must be exactly as expressive as the mockup — no more, no less —
because the mockup is the design.

## Scope

In: tag normalisation on write, tag manager UI, aliases, Space rules (parse/evaluate), auto/saved/manual
Spaces, Space chips on home, Space editor with live preview and counts, AI Space suggestions (suggest
only), Read later as a manual Space, bulk select + 4 actions, Recently deleted with restore and delete
now.

Out: nested Spaces (folder-like UI), shared Spaces, Space-specific sorting beyond the global one,
hierarchy in tags.

## Deliverables

```
server/internal/spaces/
  rule.go        parse + validate {tags_all, tags_any, kinds, sources, after, before, text, similar_to_item}
  eval.go        rule → SQL WHERE + args (never string-concatenated)
  suggest.go     pattern detection over tags/kinds → suggestion prompt (no auto-create)
  rule_fuzz_test.go
server/internal/store/
  tags.go        normalise, resolve alias, merge (transactional), rename, delete, counts
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
  after?: string           // ISO date, created_at >=
  before?: string          // ISO date, created_at <
  text?: string            // FTS over title/summary/content/note
  similar_to_item?: string // cosine means + a threshold
}
```

Fields AND together; lists OR within a field. That is the mockup's semantics and the test oracle.
`similar_to_item` is the one addition the mockup only implies — it is in design.md H3, and it is
implemented as an embedding lookup with a fixed threshold (config, not UI) so the Space is stable
rather than drifting with a slider.

**Evaluation is SQL, built from typed fragments.** `text` goes through the same escaped FTS builder as
search. No rule field may ever be interpolated into a string.

## Tests first

1. **Rule semantics as a table**, one case per operator and per combination: `tags_all` with two tags
   requires both; `tags_any` with two requires either; `kinds + tags_any` ANDs across fields; empty rule
   matches everything; an unknown field is rejected at write time (not ignored); a date with a timezone
   is normalised; `before` is exclusive.
2. `FuzzSpaceRule`: arbitrary JSON never panics, terminates, rejects unknown operators, and produces
   SQL with exactly one placeholder per supplied value (assert the arg count matches — this is the
   injection guard).
3. `rapid`: equivalence between the SQL evaluator and a pure in-memory reference evaluator over
   generated items and rules. If they disagree, one is wrong — this is the strongest test in the phase
   and it is cheap.
4. **Tag normalisation**: `"  Web   Dev "` → `web dev`; existing canonical tag reused (no duplicate
   spelling); a new tag is created only when no alias/canonical matches; unicode and emoji survive.
5. **Merge leaves an alias**: merge `js` into `javascript` → items reassigned, `tag_aliases['js'] =
   javascript.id`, and the next write of `js` resolves to `javascript`. Merge is transactional: a
   failure mid-way leaves nothing half-moved. `rapid`: merging is idempotent and alias chains never
   cycle (resolve recursively with a depth cap).
6. **Delete a tag** removes `item_tags` rows but not items; the tag can be recreated; its aliases are
   removed.
7. **Bulk actions are atomic per batch**: 200 items, one failure → nothing changed (or a documented
   partial-with-report — **decide: all-or-nothing in one transaction**, because the UI shows one
   result).
8. **Bulk refresh** enqueues jobs, does not block, and is de-duplicated against in-flight jobs for the
   same item.
9. **Recently deleted**: list returns soft-deleted within 30 days; restore returns it to the library
   with tags and highlights intact; "delete now" purges DB + local media + R2 keys (Worker call
   faked); the purge job removes items older than 30 days and nothing else. Restore of an item whose
   canonical URL now exists in a live duplicate → **decide: merge on restore**, with the note appended.
10. **Read later** is a manual Space: membership is `space_items`, deleting the Space deletes no items,
    and the item page toggle edits membership.
11. **Suggestions**: a suggestion never creates a Space; accepting it creates one with the suggested
    rule and the user's name; dismissing it is remembered so it does not reappear nightly.

## Tasks

- [ ] `spaces/rule.go` + `eval.go` before any UI, with the reference evaluator in `_test.go`.
- [ ] Port `ruleTest` from `design/mockup/js/ui.js` as the reference *verbatim* (it is the spec) and
      keep it in the test file so drift is caught by the equivalence test.
- [ ] Tag write path: normalise → resolve alias → reuse canonical → insert. Call it from enrichment and
      from manual edits, so there is one implementation.
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
- [ ] Purge job: daily, 30 days, logs counts, calls the Worker purge route for R2 keys, removes local
      media files that are no longer referenced by any row (reference-count check before unlink).
- [ ] AI suggestions: a nightly job that looks for ≥5 items sharing a new tag or kind in 30 days and
      writes a *suggestion row*; UI shows it in the Spaces list as "suggested"; accept/dismiss.
- [ ] Update design.md §13 with the `similar_to_item` threshold and the restore-merge rule.

## Exit criteria

Automated: rule table + fuzz + the equivalence property green; merge/alias property green; bulk
atomicity test green; purge tests green; `make check` green.

Demo:

1. Imported library open on Home: chips show the imported Raindrop collections; clicking one shows
   exactly its items.
2. Create an auto Space "Long reads" = `{kinds:[article,paper], text:"", after: 90 days ago}`; the
   count is right and updates as new items land.
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
  sharing). Reference-count before unlink; test the shared-file case explicitly.
- **Chips as primary navigation** means their ordering and counts matter. Sort by item count, cache the
  counts, and never leave a chip showing a stale count after a bulk action outside the Space.

## Build-time decisions to record

- The `similar_to_item` cosine threshold and whether it is exposed (recommend: config only).
- Restore-merge vs restore-conflict behaviour.
- Whether Space counts are computed on read (cheap at this scale) or cached (invalidate on write) —
  recommend read, with a measurement at 10k items.
- Bulk batch cap for Refresh.
