# Importing an old library

Harbor reads two bookmark files, because they are the two that hold a real library:

- **Raindrop CSV** — Raindrop's own *Settings → Export* file.
- **Netscape bookmarks HTML** — what Firefox, Chrome, Safari and every bookmark tool write. Harbor's
  own `harbor export --format netscape` writes it too.

The two are detected from the file's contents, so the name does not matter. Phase 5 has a known
mapping difference between them, and it is the first thing below.

## What is preserved

| In the file | In Harbor |
|---|---|
| `url` | canonicalised into `items.url`; the raw value stays in `url_original` |
| `title` | `items.title` |
| `note` | `items.note` |
| `excerpt` | `og_json.excerpt` (the page's description, never mixed into your note) |
| `cover` | `og_json.cover` |
| `tags` | the item's **manual** tags, through the same tag path the app uses, so an alias applies |
| `folder` | a **manual Space** of that name, reused case-insensitively |
| `created` | `items.created_at` *and* `items.saved_at`, so it sits at its historical position |
| `highlights` | one `highlights` row per `Highlight:` block, anchored as `imported` |
| `id` | `items.import_ref` (provenance, and what makes a re-import a merge) |
| `favorite` | membership of the seeded **Favourites** Space |

Not preserved: Raindrop's `type`-ish extras and anything under an unknown column — those are listed in
the report rather than guessed at. Firefox's `<DD>` descriptions are not imported either: our own
export does not write them, and the plan maps URL, title and folder.

**The HTML format has no highlights.** Importing a `bookmarks.html` over a library that already has
imported highlights leaves them alone; it never treats "this format cannot say" as "delete them".

## Do it in this order

```bash
# 1. A snapshot first. Import is the one operation that can double a library.
harbor backup              # or, on the potato: docker compose exec harbor /app/harbor backup

# 2. Dry run, and read it.
harbor import ~/Downloads/<export>.csv --dry-run --report /tmp/import.json

# 3. If the numbers look right, import for real.
harbor import ~/Downloads/<export>.csv --no-enrich

# 4. Enrichment is a backlog, so let it drain when you want the bill.
harbor enrich --dry-run     # how many items the model has never read
harbor enrich               # queue them; the running serve loop drains them
#    (interactive saves are enriched ahead of the backlog: it queues at -1)
```

`--no-enrich` is the documented default for a bulk import: 1,500 items through the model is a real
cost and a long drain, and the links, notes, tags, Spaces and highlights are what you came for.

## From Settings → Import

**Settings → Import** is the same run from the browser, and the same `import_runs` row: the file is
hashed the same way, so a run started in the UI can be continued with the CLI (and the other way
round) by giving it the same file.

1. Choose or drop the export. The page reads it and shows the dry-run summary — the numbers above,
   before anything is written.
2. **Import N rows** starts the run. It keeps going if the tab closes; nothing else can start an
   import while it is going, and Settings → Import is the only place to watch it.
3. Progress is bytes read, because a checkpoint only moves every hundred records. **Stop** ends the
   run at the next record and closes it as cancelled.
4. Choose the same file again to resume from the checkpoint. The summary afterwards is downloadable
   as JSON.

*Queue these for the model afterwards* is `--enrich`, off by default for the reason above. Both
surfaces write the library the same way; the CLI is still the safer one for a file large enough that
the laptop might sleep.

## Enriching a library you imported `--no-enrich`

The import default leaves every row `ready` and unread by the model: no summary, no AI tags, kind
still the default. `harbor enrich` queues the rows whose `prompt_version` is empty — the ones no
prompt has ever written — at priority `-1`, and the server's own enrichment loop drains them behind
anything you save by hand. It does not call the model itself, so it is instant and safe to run while
the container is up.

```bash
harbor enrich --dry-run          # how many are waiting, write nothing
harbor enrich --limit 50         # a slice first, to look at the tags before the bill
harbor enrich                    # the whole backlog
```

It refuses to run without `OPENROUTER_API_KEY`: without a key the loop only marks rows ready, which
would look like enrichment and be none. An item the model has already read is never queued again —
re-enriching after a prompt change is a deliberate, separate job.

On the real library (1,476 saves) it took about two hours and $0.51: 954 read by the model, 359 videos
filed from their public metadata because YouTube walls a datacenter fetch, and 163 left in Needs
attention — PDF and image links, retail anti-bot pages, dead links, and 12 videos that no longer exist.
The 163 are phases 6 and 11's to fix; the 359 filed videos are phase 7's, which gives them transcripts.
The saves themselves are all still there either way.

## What the dry run printed

- **imported / merged** — new items vs items already in the library. A URL that appears twice in the
  file is one import and one merge, exactly as the real run will do it.
- **Spaces to create** — folders the library does not have yet.
- **unknown columns** — a column Raindrop added since this was written. Nothing is coerced.
- **warnings** — per-record findings: a date that would not parse, a ragged record. A warning is not a
  failure; the item lands without that field.
- **failed records** — with the record number and why. A bad row never stops the run.

## Doing it twice is safe

Import is idempotent: the canonical URL is the key, so a second run merges into what the first wrote.
Notes are not appended twice, tags are unioned, highlights are left exactly as they were (same ids and
timestamps), and membership is a set.

**Ctrl-C is safe.** A run records how far it has read, keyed by the file's own SHA-256, so the same
command picks up where it stopped: `resuming the cancelled run of this file from record 1,203`. A
different file is a different run, a dry run leaves no record behind, and a *finished* run is not
resumed — importing the same file again is a fresh run whose rows all merge.

## What the real library did (2026-11-23)

The measured run, on the potato, through Settings → Import: 1,476 rows imported, 0 merges, 23 Spaces
created, 268 highlights, 6 favourites. The same file a second time: **0 new, 1,476 merged**, and every
count in the library unchanged. The Netscape export of the result re-imported as 1,476 merges with the
highlights intact, and a run cancelled after 131 records resumed and finished the remaining 1,345.

The saves landed **without enrichment**: usable, and their own text is already in the full-text index,
but every kind is the default `article` and the only tags are the ones Raindrop carried. There is no
search box yet — that is phase 10 — so "find it by its text" today means the database, not the UI. Enriching them is a separate,
costed decision.

## Verifying the result

```bash
harbor export --out /tmp/harbor-check --format json   # the whole library, no network needed
```

Then check, against the dry run's numbers: the item count matches, the oldest items keep their dates
when sorted by created, and five random items have their note, tags, highlights and Space. Searching
for something that exists only in the old library should find it as soon as `--no-enrich` has written
it, because the row and its FTS entry are written together.
