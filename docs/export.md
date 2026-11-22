# The export contract

`harbor export --out DIR` writes the library as files that outlive the application
([plans/12a](plans/12-durability.md)). This file is the contract: field names here are the format, and a
restore reads them.

```bash
harbor export --out ./harbor-export-20261122                  # library.json + manifest.json
harbor export --out ./harbor-export-20261122 --include-media   # plus media/<sha256>
```

**Guarantees**, in the order they matter:

1. **No network.** No Worker, no R2, no model. It reads SQLite and, with `--include-media`, the media
   directory. An export that needs anything else is not an export.
2. **Everything.** Soft-deleted items are included: "Recently deleted" is still the library.
   Embeddings are not: they are regenerated from text, and exporting them would imply they are the
   copy worth keeping.
3. **Deterministic.** Two exports of an unchanged library are byte-identical — stable ordering
   (item `seq`, tag name, `space` id, request order for highlights, `sha256` for media), fixed
   timestamps from the database, no wall-clock except `exported_at`. A diff between two exports means
   the library changed.
4. **Verified.** `manifest.json` carries the byte count and sha256 of every other file. The manifest
   never lists itself. A media blob that does not hash to its own content address fails the export,
   because copying it would put corrupt bytes under a name the manifest then vouches for.
5. **New copy only.** `--out` must be a new or empty directory. Exporting on top of yesterday's copy
   would leave neither version intact. The one directory it will not write into is the current one
   (`.`): the export has to replace its destination, and replacing the directory a shell is sitting in
   would leave that shell in a deleted inode, so it says so instead.
6. **All or nothing.** The export is built in a staging directory beside the destination and renamed
   into place only when every file is written. A failure — a missing or corrupt blob, a full disk —
   leaves nothing behind, so the retry into the same `--out` is never refused for debris.

## What is written

```
DIR/
  library.json     every item, its tags, Spaces, highlights, media rows
  manifest.json    counts, per-file bytes and sha256, app and schema version
  media/<sha256>   originals by content address, when --include-media
```

`manifest.json`:

| Field | Meaning |
|---|---|
| `export_version` | the contract version; adding fields keeps it, renaming bumps it |
| `app_version` | the Harbor build that wrote it (`r<count>.<sha>`) |
| `schema_version` | the highest applied migration, so a restore can refuse a file it cannot read |
| `exported_at` | RFC 3339 UTC |
| `counts` | items, tags, spaces, highlights, media |
| `files` | `path` (slash-separated, relative to `DIR`), `bytes`, `sha256` |

`library.json`:

| Field | Meaning |
|---|---|
| `export_version`, `app_version`, `schema_version`, `exported_at`, `counts` | as above |
| `spaces[]` | `id`, `slug` (system Spaces only), `name`, `kind` (`auto`/`saved`/`manual`), `rule` (raw JSON), `created_at` |
| `items[].id` | the public id (a UUID); the key a restore matches on |
| `items[].url` | the canonical URL, `null` for a note |
| `items[].url_original` | exactly what was saved, before canonicalisation |
| `items[].title`, `note`, `summary` | the owner's and the model's words, as stored |
| `items[].kind`, `kind_source` | one of the twelve kinds; `manual` once the owner has edited it |
| `items[].source`, `import_ref` | where the save came from; the Raindrop id or bookmark path, for re-import |
| `items[].created_at`, `saved_at`, `updated_at`, `deleted_at` | three clocks, RFC 3339 UTC; `deleted_at` present only when soft-deleted |
| `items[].tags[]` | canonical tag names, sorted |
| `items[].spaces[]` | `id` and `name` per Space. A restore matches on the **id**: `spaces.name` is not unique in the schema, so two Spaces called "Recipes" would otherwise be indistinguishable; the name is there so the file reads without joining |
| `items[].details`, `og` | raw JSON from extraction and JSON-LD (`{ }` when empty) |
| `items[].content_text`, `content_md` | the extracted text and the reader's Markdown |
| `items[].transcript` | `text`, `lang`, `state`; absent for items without one |
| `items[].highlights[]` | `id`, `quote`, `note`, `color`, `anchor` (raw JSON), `detached`, `created_at` |
| `items[].media[]` | `id`, `kind`, `filename`, `content_type`, `bytes`, `sha256`, `width`, `height`, `path` (relative to `data/media`) |

Empty arrays are written as `[]`, never `null`, so a consumer does not need to distinguish them.

## Changing it

- **Adding** a field is backwards-compatible: keep `export_version`.
- **Renaming or removing** one is a breaking change: bump `export_version`, and say in
  [plans/12](plans/12-durability.md) what a reader of the old version should do.
- The golden files in `server/internal/export/testdata/golden/` are the test. Regenerate with
  `go test ./server/internal/export -update` and **read the diff** before committing it — a golden
  that changes without a matching code change is a bug, not an update.

## Not here yet

Markdown (`markdown/0001-slug.md`) and `bookmarks.html` (Netscape) are the next slice: they are the
formats that make the export *readable* rather than restorable, and the importer in phase 5 reads the
Netscape one, so the round-trip test lands with it.
