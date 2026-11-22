# The runbook

Written for a tired person at 2 a.m. Every command here is one this binary actually has; if one
disappears, this file is wrong and the change that removed it has to fix it.

**The one sentence that matters:** `harbor.db` is the irreplaceable thing. Everything else —
containers, images, the Worker, R2, the frontend — can be rebuilt, re-pulled or replaced. Back it up,
verify the backups, and never run two Harbors against one data directory.

---

## Where things live

| What | Where |
|---|---|
| The library | `~/Harbor/data/harbor.db` (+ `-wal`, `-shm` beside it) |
| Media | `~/Harbor/data/media/<sha256>` |
| Backups | `~/Harbor/data/backups/harbor-<UTC>.db.zst`, thirty of them |
| Pre-restore snapshots | `~/Harbor/data/backups/pre-restore-<UTC>.db` |
| The lock | `~/Harbor/data/.lock`, held by `serve` for its whole life |
| Compose project | `harbor` |
| Port | 8897 → 8080, loopback, behind `tailscale serve` |

---

## Daily checks

```bash
harbor backup --list      # is last night's backup there, and is it the size you expect?
harbor backup --verify    # do they all still open?
```

The nightly job is `harbor backup --upload`: it takes the snapshot, prunes the local thirty, and sends
the new one through the Worker to the `harbor-backups` bucket (the potato holds no R2 credential). If
the Worker is unreachable the local backup still happens and the command exits non-zero — the copy
that survives the potato is the one worth being loud about.

`--verify` exits non-zero if any backup does not open. A non-zero exit on a timer run is the alarm,
not noise.

---

## Take a backup right now

```bash
# The image's entrypoint is /app/harbor, so `exec` needs the path and `run` does not.
docker compose exec harbor /app/harbor backup
# or, on the host with the container stopped:
harbor backup --keep 30
```

It prints the new file and how many are kept. If it prints `warning: … did not verify and was kept`,
a backup that was too old to keep could not be verified and was **not deleted**. Look at it before
deleting it by hand: it may be the only one that opens.

---

## Restore from a backup

**Stop the container first.** The restore refuses to run while another Harbor holds `data/.lock`, and
that refusal is the feature.

```bash
docker compose stop harbor

# `run` goes through the entrypoint (/app/harbor), so the subcommand is `restore`,
# not a path to the binary.

# 1. Look first. This changes nothing.
docker compose run --rm harbor restore \
    /data/backups/harbor-20261122T180000Z.db.zst --dry-run

# 2. Then do it.
docker compose run --rm harbor restore \
    /data/backups/harbor-20261122T180000Z.db.zst --yes

docker compose up -d harbor
```

The restore, in order: takes the lock, verifies the backup, **preserves the current database** (a
`pre-restore-<UTC>.db` snapshot when it opens, a moved-aside `pre-restore-<UTC>.corrupt/` directory
when it does not), decompresses into a temp file beside the database, verifies it again, and renames
it into place. Then it prints the replay command to run next.

Everything the Worker queued after the backup was taken is missing from the restored library, and the
save path of phase 12's invariant 9 is how it comes back:

```bash
harbor replay --since <the RFC 3339 time the restore printed>
```

**If it refuses:** read the reason. `the database is in use` means the container is still running.
`restore: … does not verify` means the backup is bad and the library was not touched.

---

## Restore drill (run it before you need it)

On a machine that is not the potato, so the real library is never at risk:

```bash
mkdir -p /tmp/drill && cd /tmp/drill
cp ~/Harbor/data/backups/harbor-<newest>.db.zst .
mkdir data
HARBOR_DATA_DIR=/tmp/drill/data HARBOR_DEPLOYMENT=dev HARBOR_DEV_NO_AUTH=1 \
  harbor restore ./harbor-<newest>.db.zst --yes
HARBOR_DATA_DIR=/tmp/drill/data HARBOR_DEPLOYMENT=dev HARBOR_DEV_NO_AUTH=1 harbor serve
```

Then check, in the browser and with `harbor export`: item count, tags, Spaces, one image, one PDF,
one highlight with its note, and search for a phrase you know is in an article body.

Record the wall-clock time here each time it is run. **The first drill has not been run yet** — the
phase-12 exit criterion is this drill, not the code that makes it possible.

| Date | Backup | Box | Time to a working library | Notes |
|---|---|---|---|---|
| — | — | — | — | not yet run |

---

## Update and roll back

The update watcher pulls by digest and drills the rollback; its record is in
[plans/00-foundations.md](plans/00-foundations.md#deploy-record-2026-11-19) (176 s, 2026-11-19).

```bash
cd ~/Harbor
docker compose pull && docker compose up -d      # or let the watcher do it
docker compose logs -f harbor                    # watch for "listening"
curl -sf localhost:8897/healthz                  # the two-field health answer
```

A failed update rolls back to the pinned digest. A failed *migration* does not roll back the database:
restore the `pre-update.db` snapshot the update took, then pull the previous digest.

---

## Disaster recovery

| Lost | Recoverable? | How |
|---|---|---|
| The container | Yes | `docker compose up -d` |
| The image | Yes | it is on `ghcr.io`, digest-pinned in `.env` |
| The Worker | Yes, with a gap | saves made while it was down are lost; everything already ingested is on the potato |
| R2 | Yes, for media | originals are content-addressed on the potato once mirrored; remote-only intake blobs are the gap |
| The potato | Yes, if the backups left it | restore the newest `harbor-<UTC>.db.zst` and the media directory; run `harbor replay --since` |
| The potato **and** the backups | Only what you exported | `harbor export --out … --include-media` is the copy that needs nothing else |

That last row is why the export exists. Keep one somewhere that is neither the potato nor R2.

---

## When something looks wrong

| Symptom | Look at |
|---|---|
| Saves do not appear | `harbor serve` logs: is the ingest loop polling, and does the Worker answer? |
| Items stay `queued` | the enrichment loop; `OPENROUTER_API_KEY` set? |
| Items go `failed` | the attention strip on Home, and the log line carrying the cause |
| Backups stop appearing | `harbor backup` by hand: the error is printed, not swallowed |
| `data/.lock` exists but nothing runs | harmless: the lock dies with the process, and the file is only a handle |
| Disk full | export, prune archives, delete media; the storage guardrail lands in 12b |
