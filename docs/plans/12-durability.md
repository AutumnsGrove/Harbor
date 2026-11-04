# Phase 12 — Durability: export, backup, restore, status

**Status:** planned · **Design refs:** [§11](../design.md#11-operations),
[E2/E3/M4/L4](../open-questions.md) · **Depends on:** phase 2 · **Gate:** none · **Est:** ongoing, but the first pass is a weekend. This
phase is built in **two passes** (below): 12a lands before the first real import, 12b alongside the
later phases.

## Goal

Harbor becomes trustworthy enough to rely on daily: an export that is readable without the app, a
nightly backup that goes off-box, a restore that has actually been run, and a Status page that answers
"is it working?" without reading logs.

This is the phase that makes the whole project's premise true — *your library, in a file you own, that
no product can take away*. If export or restore is weak, everything above it is a demo.

## Scope

In: `harbor export` (JSON + Markdown + Netscape HTML + media + manifest), `harbor backup` (VACUUM INTO →
zstd → R2, 30 dailies), `harbor restore` (CLI, container stopped), rsync to the MacBook Air, the
restore drill, Status page, storage guardrails, log hygiene, `/api/status` for Meridian, the nightly
maintenance job (purge, mirror drain, re-embed, transcript refresh hooks), link-check scheduling
visibility.

Out: point-in-time recovery, a restore button, backup encryption (settled: none extra), cross-version
export compatibility guarantees beyond one minor version back.

## Two passes

**12a — before the first real import (milestone M4).** The irreplaceable data arrives with phase 5,
so this is what must exist first: `harbor snapshot` (already there from phase 0) with retention,
the Worker's `/backups` routes and the nightly upload, `harbor restore`, `harbor replay` usage in the
restore procedure, the **restore drill actually run on a clean directory**, `harbor export` (JSON,
Markdown, Netscape HTML, media, manifest) with the golden-fixture CI job, and the first version of
`docs/runbook.md`.

**12b — alongside phases 6–11.** The Status page and `/api/status`, the maintenance tasks registered
in the scheduler, disk guardrails, log redaction, Meridian wiring (only once `/healthz` and
`/api/status` are real on the box), and the runbook's remaining sections.


```
server/cmd/harbor/
  export.go     flags: --out, --format=json|md|html|all, --include-media
  backup.go     --now, --list, --verify
  restore.go    <snapshot>, prints a plan, takes a pre-restore snapshot of the current DB, refuses to
                run unless it can take the exclusive lock on data/.lock (which `serve` holds for its
                whole life — this is how "the DB is in use" is detected portably), and finishes by
                printing the exact `harbor replay --since <snapshot time>` to run next
server/internal/backup/
  snapshot.go   thin wrapper over phase 0's store.Snapshot (VACUUM INTO temp → fsync → rename →
                integrity check); this package adds retention, listing and upload
  retention.go  keep 30 daily, delete older, never delete a snapshot that fails verification
  upload.go     zstd (pure-Go klauspost/compress) → R2 via the Worker's `/backups` routes (the potato
                holds no R2 credentials), multipart because a snapshot can exceed one request body
server/internal/export/
  json.go markdown.go netscape.go manifest.go
  testdata/     a golden library fixture → golden export, diffed in CI
server/internal/maintenance/
  nightly.go    tasks registered in the in-process scheduler (state in task_runs): purge, mirror
                drain, re-embed sweep, transcript refresh, disk check, backup
server/internal/httpapi/status.go   /api/status (counts only, loopback)
web/src/routes/settings/data/+page.svelte     export, backup list, storage
web/src/routes/settings/status/+page.svelte   the operator's page
docs/runbook.md      deploy, update, rollback, backup, restore drill, disaster recovery
deploy/harbor-rsync.sh      host-side copy to the MacBook Air (pulled by the Mac when it is awake, or
                            pushed from the potato if reachable); the backup itself runs in the scheduler.
                            systemd is used only for this and the update watcher
```

## Export contract

`harbor export --out ./harbor-export-YYYYMMDD` produces:

```
harbor-export-YYYYMMDD/
  library.json          items with every field, tags, spaces, highlights, media manifest (no blobs)
  markdown/0001-slug.md one file per item: front-matter (url, kind, tags, dates), note, summary,
                        extracted text, highlights as blockquotes with the annotation beneath
  bookmarks.html        Netscape: url + title + folder = first manual Space, ADD_DATE = created_at
  media/<sha256>        originals, content-addressed (optional with --include-media)
  manifest.json         counts, sha256 of every file, the app version, the schema version
```

Rules that make it a real export rather than a data dump:

- **Readable without the app.** A Markdown file per item, openable in any editor; images referenced
  relatively; no proprietary structure required to understand the library.
- **Restorable.** `library.json` + media + the schema version is enough to rebuild; the manifest
  verifies it.
- **Stable.** Field names are part of the contract; adding fields is fine, renaming is a breaking
  change that bumps `export_version`.
- **Never requires the network.** No Worker, no R2, no model. It reads SQLite and the media dir.
- **Highlights and notes are included**, because they are the parts a user cannot regenerate.

## Tests first

1. **Golden export** from a fixture library (items of every kind, one note-only, one with highlights, a
   PDF, a shared media file, an item with a very long text): `json`, `markdown`, `bookmarks.html`, and
   the manifest are diffed against committed goldens. `-update` regenerates, and the diff is reviewed.
2. **Round-trip**: export → import via the Netscape and JSON paths → the library is equivalent (item
   count, canonical URLs, notes, tags, highlights, dates). Not byte-identical; equivalence is the
   property. `rapid` on a generated small library.
3. **Manifest verification**: a corrupted media file is detected; a missing file is detected; a
   truncated `library.json` fails with a clear message rather than half-writing.
4. **Snapshot**: `VACUUM INTO` on a live DB with an open writer succeeds (this is the *reason* for
   `VACUUM INTO` over `.backup` — the coffer livelock); the snapshot passes `PRAGMA integrity_check`
   and contains the rows committed before it started.
5. **Retention**: 31 snapshots → 30 remain, oldest deleted; a snapshot that fails verification is never
   deleted (fail loud) and the run aborts the prune.
6. **Restore**: restore a snapshot into a temp dir, open it, compare counts + a spot-checked item +
   one media file → success. Restore refuses while another process holds `data/.lock` (start a
   `serve` against the dir and assert it refuses); it prints what it will overwrite and requires
   `--yes`. **Restore + replay:** restore a snapshot taken before ten fake-Worker jobs completed,
   run `replay --since`, and the ten items reappear with no duplicates.
7. **Status endpoint**: shape exactly as [meridian.md](../meridian.md) specifies, counts only, no
   titles/URLs/tags; exempt from the identity lock only when the Tailscale header is absent (design
   §3 — "loopback-only" cannot be tested inside Docker); never touches a model or the Worker; responds <100 ms on 10k items.
8. **Nightly maintenance** is idempotent: running it twice in a row changes nothing the second time
   (purge counts 0, mirror drains 0, re-embed 0), and a component failure doesn't stop the others.
9. **Disk guardrail**: at the configured threshold the UI warns; it never blocks a save.

## Tasks

**Export**

- [ ] JSON writer with an `export_version` const and a documented field contract in `docs/export.md`.
- [ ] Markdown writer with front-matter, escaping, slug uniqueness, and highlights.
- [ ] Netscape writer (the same shape the phase 5 importer reads — assert with a round-trip test).
- [ ] Media copy by sha with a manifest of hashes and sizes.
- [ ] CLI + Settings → Export (download a tar.gz for small libraries; the CLI is the documented path
      for a full one).
- [ ] The CI job that runs export against the fixture on every PR (design.md §5's "tested in CI with a
      fixture from day one").

**Backup and restore**

- [ ] `snapshot.go` with temp → fsync → rename, integrity check, and a pre-update hook reused by the
      update watcher from phase 0.
- [ ] zstd compression, 30-daily retention, and a listing that shows date/size/version/integrity.
- [ ] Upload via the Worker's token-gated route (the potato holds no R2 credentials) with retry and a
      `last_backup` timestamp that the Status page and `/api/status` read.
- [ ] `deploy/harbor-rsync.sh` for the MacBook Air (host-side; the distroless image has no rsync),
      reachability-guarded, recording the last successful copy where the Status page can read it.
- [ ] `harbor restore` with a dry-run plan, `--yes`, a pre-restore snapshot of the *current* DB (so a
      bad restore is itself recoverable), and a refusal to run against an open DB.
- [ ] `docs/runbook.md` with the drills, written so a tired person at 2 a.m. can follow it.
- [ ] **Run the drill for real**, on a clean directory, and paste the timings into the runbook.

**Status and guardrails**

- [ ] `/api/status` exactly per meridian.md; `/healthz` still static and two-field.
- [ ] Settings → Status: last poll, queue depth, needs-attention count, mirror pending, last backup,
      last link check, vector cache state, disk used, item count, version, next maintenance.
- [ ] Structured JSON logs with request ids; no titles, URLs, notes, or token material in logs (a test
      that scans the log encoder's output for a seeded secret).
- [ ] Storage guardrail warning at a configurable threshold, with a one-line "what to do" (export,
      prune archives, delete media).
- [ ] Meridian wiring on deploy day per the checklist (after `/healthz` and `/api/status` are real),
      not before.
- [ ] `docs/runbook.md` covers: deploy, update, rollback, backup verify, restore, lost Worker, lost R2,
      lost potato (what's recoverable and what isn't).

## Exit criteria

Automated: golden export diff green in CI; round-trip equivalence green; snapshot/retention/restore
tests green; status shape test green; log-redaction test green; `make check` green.

Demo, the drill that decides whether the project works:

1. `harbor export` a real library → open three Markdown files in a plain editor, open
   `bookmarks.html` in a browser, verify images render, verify a highlight is present with its note.
2. Re-import the export into an empty Harbor → equivalent library; note the diff (should be zero).
3. `harbor backup --now` → snapshot created, verified, uploaded; `--list` shows 30-day retention.
4. **Restore drill on a clean box:** stop the container, `docker compose run --rm harbor restore
   <snapshot> --yes` into a fresh `data/`, run `harbor replay --since <snapshot time>`, start, and confirm: item count, tags, spaces, highlights, one image and one PDF open,
   search works, and the mirror state is correct. Time it; put the number in the runbook.
5. Corrupt one media file in the export and confirm the manifest catches it.
6. Disconnect the network for a night → the backup fails loudly, the Status page shows a stale
   `last_backup` (amber), and the next night succeeds.
7. Read the Status page cold and answer: is the poll loop working, is anything stuck, when was the last
   backup, how much disk is left? If any answer is missing, the page is not done.
8. Confirm `/healthz` returns in <10 ms while a backup and an enrichment burst are running.

## Risks

- **A backup that has never been restored is a hypothesis.** The drill is the deliverable, not the
  script. Run it on a clean box, documented, with a date.
- **The DB and the media must be restored together.** Design says this explicitly; the export manifest
  and the runbook must too. Restoring the DB alone produces a library of broken thumbnails.
- **Worker-dependent backup creates a single point of failure.** If the Worker is down for a night, the
  snapshot still exists locally — the order is: snapshot, verify locally, *then* upload; a failed upload
  is retried from the local copy, never by taking a new snapshot only.
- **SD-card wear.** A nightly full-database `VACUUM INTO` writes the whole DB; keep only the last 3
  snapshots on the potato (30 live in R2), and let the Status page show snapshot size.
- **Retention deleting the only good copy** is the classic disaster. Never prune a snapshot that fails
  verification; keep one snapshot per month beyond the 30 dailies if it is cheap (decide).
- **`/api/status` becoming a slow page.** Counts must come from cheap queries or cached counters; on a
  10k library a naive `COUNT(*)` on several tables per Meridian probe is fine, but on a big media table
  it isn't. Measure.
- **Export on a huge library** (say 20 GB of media) needs to stream and be resumable; a tar.gz in
  memory would kill the potato. Stream to disk, and document the disk headroom requirement.

## Build-time decisions to record

- Whether monthly snapshots are kept beyond the 30 dailies.
- Whether media is included in the default export or requires `--include-media` (recommend: required
  flag, with the runbook showing the full command).
- rsync cadence and whether it runs from the potato or is pulled by the Mac.
- The exact `/api/status` counter caching strategy.
