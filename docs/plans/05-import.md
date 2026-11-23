# Phase 5 — Import: the old library moves in

**Status:** planned, gate [S9](spikes.md#s9--a-real-raindrop-export) answered 2026-11-22 · **Design refs:** [§5](../design.md#5-save-path) (Import),
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
server/internal/importer/   (`import` is a Go keyword, so the package cannot be named that)
  csv.go           Raindrop CSV reader (header-tolerant, quoted fields, BOM, CRLF)
  html.go          Netscape bookmark HTML (nested <DL>, folders, ADD_DATE, ICON)
  import.go        the driver: parse → canonicalise → merge → space assignment → enqueue
  progress.go      counters + resume checkpoint (imported URL set is the idempotence key)
  testdata/raindrop/*.csv     fixtures in the real shapes (real-shape.csv is hand-built, S9), plus
                              synthetic edge cases: BOM, CRLF, no header, broken rows
  testdata/netscape/*.html    nested folders, duplicates, no dates
server/cmd/harbor/import.go    CLI flags: --dry-run, --no-enrich, --limit, --report
server/internal/httpapi/import.go   start/status/cancel, streams progress
web/src/routes/settings/import/+page.svelte   drop file, dry-run summary, run, per-row report
web/src/routes/first-run/+page.svelte         now live: Import or first link
```

## Raindrop CSV mapping

Confirmed against a real export by [S9](spikes.md#s9--a-real-raindrop-export) on 2026-11-22: the
columns are exactly `id,title,note,excerpt,url,folder,tags,created,cover,highlights,favorite`, no
unknowns, one flat `folder` per row, and a `highlights` cell that is prose (see below).

| CSV column | Harbor |
|---|---|
| `title` | `items.title` (fallback: extracted title later) |
| `note` | `items.note` |
| `excerpt` | `og_json.excerpt` — keep the user's note clean |
| `url` | canonicalised into `items.url`; the raw value stays in `items.url_original` |
| `folder` | manual Space (name preserved); flat, because the export has no nesting |
| `tags` | `item_tags` with `source='manual'` (they are the user's, not the model's), written through the phase 3 tag write path so aliases and canonical names apply |
| `created` | `items.created_at` **and** `items.saved_at` (preserved; this is what makes the import feel like the old library and keeps it out of "recent"); RFC 3339 with milliseconds |
| `cover` | `og_json.cover` (the image itself is fetched in phase 6 if it is a URL) |
| `highlights` | one `highlights` row per `Highlight:` block: `quote` = the block text without the prefix, `note` empty, `anchor_json` marked imported (S9: no colour, no per-highlight note, no delimiter — split only on a blank line *followed by* `Highlight:`) |
| `id` | `items.import_ref` (idempotence aid and provenance) |
| `favorite` | `space_items` membership of the seeded manual Space **Favourites** (`slug='favourites'`), and sticky on merge |

Unknown columns are ignored and listed in the report, so a schema change in Raindrop's export is
visible rather than silent. A row with no URL and no title is skipped and counted. A missing header
is a typed error naming the columns it expected, never a positional guess.

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
5. **Highlights**: a CSV row with two `Highlight:` blocks creates two `highlights` rows anchored to
   the item with `anchor_json` marked `imported` (no text to anchor against yet); they render on the
   item page without a reader. A block containing its own blank lines stays **one** row.
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

- [x] Parser first, CLI second, UI last. The CLI is the real interface for a big file.
- [x] `--dry-run` prints the full summary (counts, new vs merge, spaces to create, unknown columns) and
      writes nothing — this is what gets run against the *real* export before anything is committed.
- [x] Import writes items with `state='ready'` when `--no-enrich` (they are usable immediately) and
      otherwise queues them at `priority=-1`, but **does not block** on enrichment: the run finishes,
      enrichment drains afterwards behind anything interactive.
- [x] Collections → `spaces.kind='manual'`, `space_items` membership; an existing Space with the same
      name is reused (case-insensitive) rather than duplicated.
- [x] Settings → Import: file drop, dry-run, run with a progress bar, cancel, and a downloadable
      report. Progress is polled, not streamed — simpler and enough.
- [x] First-run screen now offers Import; it is the first real use of the empty state. It is the home
      feed's empty state rather than a `/first-run` route: design.md §9 is one surface, not two, and
      an empty library already *is* the first run.
- [x] A `docs/import.md` with: how to export from Raindrop, the exact expected filename, what is
      preserved, what is not, and how to verify the result.
- [x] Record the mapping decisions in design.md §5 (same PR as the S9 verdict).

## Where the build is (2026-11-22)

S9 is answered, so the mapping table above is measured rather than remembered. What exists:

- **Parsers** — `server/internal/importer` reads Raindrop CSV and Netscape HTML into one `Row` shape.
  A malformed record is a warned row, never the end of the run; a headerless file is a typed error
  naming the column it wanted. `FuzzImportRow` and `FuzzImportHTML` carry committed seeds.
- **Store writes** — the Favourites Space (migration `0002`), `EnsureManualSpace`, `AddItemToSpace`,
  `ReplaceImportedHighlights` (idempotent, and never touches the owner's own highlights),
  `HighlightsForItem`, and the read-only `FindManualSpace` a dry run needs.
- **Driver** — canonicalise → merge → tags through the shared write path → the folder as a manual
  Space → Favourites → highlights → `priority=-1`, with a dry run that writes nothing, a report whose
  counters add up, and per-row failures that never stop the run. A source that cannot express
  highlights (the HTML one) never clears the ones a CSV import wrote.
- **CLI** — `harbor import FILE` with `--dry-run`, `--no-enrich`, `--limit` and `--report`, detecting
  CSV or HTML from the file's contents; `docs/import.md` is the operator's page for it.

**The real export has been run** (2026-11-22), on a throwaway database, with the export S9 measured:
`harbor import <export>.csv --dry-run` reports 1,476 of 1,476 rows, 0 merges, 23 Spaces to create,
268 highlights, 6 favourites, and no warnings, failures or unknown columns. The real run then wrote
1,476 items (all `source=import`, `priority=-1`, `state=ready` under `--no-enrich`), 1,482 Space
memberships, 268 highlights and 2,051 canonical tags (down from 2,134 raw strings — `3D`/`3d` and
punctuation collapse), with dates preserved from 2024-03-21 to 2026-11-14. A second run imported 0
and merged 1,476, created no Spaces and left the library identical, so idempotence holds on the real
file and not only under `rapid`. 23 Spaces rather than 24 folders is the `Read Later` folder reusing
the seeded Read later Space case-insensitively, which is the intended behaviour.

- **Resume** — `import_runs` is keyed by the file's sha256, so a cancelled run resumes from its
  checkpoint instead of re-reading rows it already applied; a dry run leaves no run behind, and a
  finished run is a fresh run (whose rows all merge). The checkpoint flushes the highlights it has
  read first, so resuming can never skip past a row whose highlights were never written.

- **Settings → Import** — `POST /api/import` takes the file as the body (dry run or a background
  run), `GET /api/import/status` polls the run from `import_runs` plus the bytes read, and
  `POST /api/import/cancel` stops it at the next record. One run at a time; a cancel that arrives
  before the run row is open is remembered rather than lost; `/api/busy` reports a running import so
  an update does not restart mid-file. `web/src/routes/settings/import/+page.svelte` is the page:
  drop the file, read the dry run, import, watch, stop, download the report. The home feed's empty
  state now links to it.
- **Shared run protocol** — `importer.RunTracked` and `importer.NewSource`, so the CLI and the API
  cannot drift on the three things that lose rows: resume from the checkpoint, write it on a context
  an interrupt cannot kill, and close the run as done or cancelled.

## The exit demo, run on the potato (2026-11-23)

Run from **Settings → Import** in a real browser over the tailnet, on the box, with the export S9
measured (`3cad2d1c-….csv`, 931 kB). Before it: the box updated itself from `r197` to `r206` through
its own update path (check → start, 22 s, snapshot kept), and a backup was taken.

| Exit criterion | Result |
|---|---|
| 1. Dry run read first | 1,476 rows, 0 merges, 23 Spaces to create, 268 highlights, 6 favourites, no warnings, no unknown columns |
| 2. Real run matches the dry run; dates kept | 1,476 imported, 0 merged, 23 Spaces created, 268 highlights, 6 favourites; dates run 2024-03-21 → 2026-11-14 |
| 3. The same import again | 0 new, 1,476 merged, 0 Spaces, same item count, same tags and highlights |
| 4. Five random items by hand | titles, notes, tags, source and saved date all render; each sits in its collection-Space; highlights are stored (268 rows) but the item page cannot list them until phase 8 |
| 5. Netscape HTML twin | `harbor export --format netscape` of the imported library, imported: 0 new, 1,476 merged, and the 268 highlights were **not** cleared |
| 6. Cancel and resume | cancelled after 131 records; the same file resumed and merged the remaining 1,345, finishing at checkpoint 1,476 |
| 7. Search before enrichment | cannot be run: search is phase 10 |

The library afterwards: 1,476 items (all `source=import`, all `state=ready`, kinds still the default
`article`), 25 Spaces, 1,482 memberships, 268 highlights, 2,051 canonical tags, 7,412 item tags. The
second import left every one of those numbers unchanged, which is the property the whole phase is
about.

Two exit criteria in the list above are **not this phase's to prove**, and the plan was wrong to put
them here: "highlights listed" needs phase 8's item-page highlights, and "search … before enrichment"
needs phase 10. They belong to those phases' demos; this phase's page can only show that the rows
exist, which the database does.

The imports are **not enriched**. Everything landed `state=ready` under the documented `--no-enrich`
default, at ~$2 for the whole file if it is ever queued. That decision is Autumn's, and it is the next
thing this phase's data wants.

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
  (`harbor backup`). Do not do the first real run until milestone M4 (phase 12a): the restore
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

- `excerpt` destination: `og_json.excerpt`.
- Folder naming: flat, name preserved (the real export has no nesting; a Netscape import path-joins).
- Highlights: one row per `Highlight:` block, quote = block minus prefix, `anchor_json`
  `{"source":"imported","index":n}`; no colour guessing, no annotation guessing (S9).
- Favourites are a real thing, not a report line: the phase's migration seeds a manual Space named
  Favourites (`slug='favourites'`), imported `favorite=true` rows join it, and merging a favourite
  never removes it. The star toggle and Space UI come with phase 9.
- Import sets `source='import'` (design.md §7) and the source filter shows it.
- The `--no-enrich` default and the documented cost of a full enrich of an import.
