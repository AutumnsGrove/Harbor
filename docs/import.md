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
make backup-now            # or: harbor backup --now

# 2. Dry run, and read it.
harbor import ~/Downloads/<export>.csv --dry-run --report /tmp/import.json

# 3. If the numbers look right, import for real.
harbor import ~/Downloads/<export>.csv --no-enrich

# 4. Enrichment is a backlog, so let it drain when you want the bill.
#    (interactive saves are enriched ahead of it)
```

`--no-enrich` is the documented default for a bulk import: 1,500 items through the model is a real
cost and a long drain, and the links, notes, tags, Spaces and highlights are what you came for.

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
timestamps), and membership is a set. A cancelled run is safe to repeat — it restarts from the top
rather than resuming, which is slower but never wrong.

## Verifying the result

```bash
harbor export --out /tmp/harbor-check --format json   # the whole library, no network needed
```

Then check, against the dry run's numbers: the item count matches, the oldest items keep their dates
when sorted by created, and five random items have their note, tags, highlights and Space. Searching
for something that exists only in the old library should find it as soon as `--no-enrich` has written
it, because the row and its FTS entry are written together.
