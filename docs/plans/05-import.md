# Phase 5 — Import: the old library moves in

**Status:** planned · **Design refs:** [§5](../design.md#5-save-path) (Import),
[I1](../open-questions.md), [§9](../design.md#9-ui-sveltekit-embedded) (first run)
**Depends on:** phases 2, 3 (the tag write path) and **12a** (snapshot, backup and the restore
drill must exist before the irreplaceable data does) · **Gate:**
[S9](spikes.md#s9--a-real-raindrop-export) · **Est:** a weekend. Manual Spaces are created here, so
phase 9 is not required.

## Goal

Raindrop CSV and Netscape HTML import, so cancelling the subscription is real rather than
aspirational. Import is idempotent, resumable, and runs through the normal enrichment queue — no
special pipeline, no "imported" limbo. Collections and folders become manual Spaces. Highlights
survive, because Raindrop highlights are the one thing in the export that is tedious to recreate.

This is also the phase that stress-tests every phase-2 assumption at 10× the volume: merge semantics,
FTS writes, and the queue's idempotence all get a real workout.

## Scope

In: `import` package (CSV + HTML parsers), CLI (`harbor import`), Settings → Import with progress,
idempotent merge, collection/folder → manual Space, highlight import, dry-run and skip report,
resumability, first-run wiring.

Out: mymind (dropped), automatic watching of an export directory, other bookmark formats.

## Deliverables

```
server/internal/import/
  csv.go           Raindrop CSV reader (header-tolerant, quoted fields, BOM, CRLF)
  html.go          Netscape bookmark HTML (nested <DL>, folders, ADD_DATE, ICON)
  import.go        the driver: parse → canonicalise → merge → space assignment → enqueue
  progress.go      counters + resume checkpoint (imported URL set is the idempotence key)
  testdata/raindrop/*.csv     real-shaped fixtures (sanitised from the S9 export): normal, folded, unicode, broken rows
  testdata/netscape/*.html    nested folders, duplicates, no dates
server/cmd/harbor/import.go    CLI flags: --dry-run, --no-enrich, --limit, --report
server/internal/httpapi/import.go   start/status/cancel, streams progress
web/src/routes/settings/import/+page.svelte   drop file, dry-run summary, run, per-row report
web/src/routes/first-run/+page.svelte         now live: Import or first link
```

## Raindrop CSV mapping

| CSV column | Harbor |
|---|---|
| `title` | `items.title` (fallback: extracted title later) |
| `note` | `items.note` |
| `excerpt` | appended to note as a quoted block, or `og_json.excerpt` — **decide: `og_json`**, keep the user's note clean |
| `url` | canonicalised into `items.url` |
| `folder` | manual Space (name preserved) |
| `tags` | `item_tags` with `source='manual'` (they are the user's, not the model's), written through the phase 3 tag write path so aliases and canonical names apply |
| `created` | `items.created_at` **and** `items.saved_at` (preserved; this is what makes the import feel like the old library and keeps it out of "recent") |
| `cover` | `og_json.cover` (the image itself is fetched in phase 6 if it is a URL) |
| `highlights` | `highlights` rows: `quote`, `note`, colour default |
| `id` | `items.import_ref` (idempotence aid and provenance) |
| `favorite` | reported in the dry-run summary, not imported |

Unknown columns are ignored and listed in the report, so a schema change in Raindrop's export is
visible rather than silent. This table is the design's recollection of the export; **spike S9
replaces it with the real column list before any importer test is written.** A row with no URL and no title is skipped and counted.

## Tests first

1. **Idempotence (the headline property).** `rapid`: for a generated fixture, importing once and
   importing twice produce identical libraries — same item count, same tags, same notes, same
   space membership, `updated_at` bumped at most once per item.
2. **Merge on import.** A URL already in the library gains the imported note (appended), unions tags,
   keeps its existing AI tags, keeps Read later membership, and keeps the *earlier* `created_at`.
3. **CSV tolerance**: BOM, CRLF, quoted commas and newlines, doubled quotes, a missing header, extra
   columns, a duplicate `url` column, an empty file. `FuzzImportRow` (native fuzz) asserts malformed
   rows are skipped with a warning and never abort the run.
4. **Netscape HTML**: nested `<DL>` folders create nested manual Spaces (or a flat space with a
   `/`-joined name — **decide: flat, name path-joined**, because nested Space UI does not exist);
   `ADD_DATE` parsed; entities decoded; a document with no `<DT>` doesn't hang.
5. **Highlights**: a CSV row with two highlights creates two `highlights` rows anchored to the item
   with `anchor_json` marked `imported` (no text to anchor against yet); they render on the item page
   without a reader.
6. **Resumability**: cancel at row 500 of 1000, restart, and the run continues without duplicates and
   without re-enriching the first 500 beyond the idempotence rule.
7. **Report**: counts of imported / merged / skipped / errors, with the first N error reasons, and a
   stable JSON shape for the UI.
8. **Volume**: a 5,000-row fixture imports in under 2 minutes on the potato with `--no-enrich` and
   does not exceed the RSS budget (record the number).
9. **Tags go through the shared write path**: an imported `js` lands on `javascript` once that alias
   exists (assert by merging first, then importing), and an unknown tag is created normalised.
10. **Priority and ordering**: imported items are queued at `priority=-1`, carry `saved_at =
    created_at`, and a link saved interactively afterwards is enriched before any of them. Progress
    and the resume checkpoint live in `import_runs`.

## Tasks

- [ ] Parser first, CLI second, UI last. The CLI is the real interface for a big file.
- [ ] `--dry-run` prints the full summary (counts, new vs merge, spaces to create, unknown columns) and
      writes nothing — this is what gets run against the *real* export before anything is committed.
- [ ] Import writes items with `state='ready'` when `--no-enrich` (they are usable immediately) and
      otherwise queues them at `priority=-1`, but **does not block** on enrichment: the run finishes,
      enrichment drains afterwards behind anything interactive.
- [ ] Collections → `spaces.kind='manual'`, `space_items` membership; an existing Space with the same
      name is reused (case-insensitive) rather than duplicated.
- [ ] Settings → Import: file drop, dry-run, run with a progress bar, cancel, and a downloadable
      report. Progress is polled, not streamed — simpler and enough.
- [ ] First-run screen now offers Import; it is the first real use of the empty state.
- [ ] A `docs/import.md` with: how to export from Raindrop, the exact expected filename, what is
      preserved, what is not, and how to verify the result.
- [ ] Record the mapping decisions in design.md §5.

## Exit criteria

Automated: idempotence property green; fuzz row corpus green; volume fixture under the time/RSS
budget; `make check` green.

Demo, with the real subscription data:

1. Export Raindrop → CSV. Run `harbor import --dry-run` against it; read the report; resolve any
   unknown columns or parse errors *before* importing.
2. Run the import for real. Total item count matches the dry run; the oldest items keep their original
   dates when sorted by created.
3. Run the same import a second time → zero new items, zero duplicates, same tag count.
4. Open five random imported items: notes intact, tags intact, highlights listed, collection present
   as a manual Space.
5. Netscape HTML export of the same library imports into the same items (merge, not duplicate).
6. Cancel mid-import and resume; the final count is correct.
7. Search for an item that only exists in the old library → found, before enrichment has finished.

## Risks

- **Import is the one operation that can double a library.** Everything about it is idempotence-first;
  run the dry run on real data, and take a `VACUUM INTO` snapshot before the first real run
  (`harbor backup --now`). Do not do the first real run until milestone M4 (phase 12a): the restore
  drill must have been run, because this is the moment the box starts holding irreplaceable data.
- **Raindrop's export changes.** The unknown-column report is the early warning. Do not silently
  coerce.
- **Volume vs the enrichment queue.** 5,000 items through DeepSeek is a real bill (~$5 at the
  estimated rate) and a long drain. Make `--no-enrich` the documented default for bulk import and let
  enrichment be a separate, monitored run.
- **Backdated dates vs "recent".** **Settled:** the home grid sorts on `saved_at`, and import sets
  `saved_at = created_at`, so the library looks like the old one rather than a fresh dump and a
  later re-save genuinely resurfaces an item. The import report links to the imported set.
- **Folio of edge cases in Netscape HTML** (some browsers emit invalid nesting). Be lenient, never
  hang; a truncated file is a typed error with the row count.

## Build-time decisions to record

- `excerpt` destination (`og_json`).
- Folder naming: path-joined flat names.
- Whether import sets `source='import'` (yes, per design.md §7) and how the source filter shows it.
- The `--no-enrich` default and the documented cost of a full enrich of an import.
