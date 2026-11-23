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

The nightly job is `deploy/harbor-backup.timer` → `harbor backup --upload`: it takes the snapshot, prunes the local thirty, and sends
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

Two drills, and they are different things.

### 1. The procedure, repeatable: `make drill`

`scripts/drill.sh` builds a library through the API, backs it up, **moves the whole data directory
away**, restores from the backup, and checks the library came back — then exports what it restored and
checks the export agrees. Everything happens in a temp directory with a dev data dir, so it cannot
touch a real Harbor, and nothing needs the network.

```bash
make drill                 # 300 items
COUNT=2000 make drill      # a bigger library
KEEP=1 make drill          # leave the temp directory behind to look at
```

Measured on this MacBook Air, 2026-11-22:

| Items | Database | Backup (zstd) | Backup | Restore | Export | Notes |
|---|---|---|---|---|---|---|
| 300 | 1.5 MB | 36 KB | 0.06 s | 0.06 s | 0.06 s | whole drill, build included: ~5 s |
| 2000 | 10 MB | — | 0.16 s | 0.12 s | 0.42 s | 2000 markdown files |

### 2. The real library, on a clean box (not yet run)

The phase-12 exit criterion is this one: the actual `harbor.db` from the potato, restored somewhere
that is not the potato. `make drill` proves the procedure and the code; it cannot prove the potato's
backup, because that backup has never been taken.

```bash
# on the Mac, with a copy of the newest backup from the potato
mkdir -p /tmp/drill2/data
cp <newest>.db.zst /tmp/drill2/
HARBOR_DATA_DIR=/tmp/drill2/data HARBOR_DEPLOYMENT=dev HARBOR_DEV_NO_AUTH=1 \
  harbor restore /tmp/drill2/<newest>.db.zst --yes
HARBOR_DATA_DIR=/tmp/drill2/data HARBOR_DEPLOYMENT=dev HARBOR_DEV_NO_AUTH=1 harbor serve
```

Then check in the browser and with `harbor export`: item count, tags, Spaces, one image, one PDF, a
highlight with its note, and a search for a phrase you know is in an article body — the last two only
once phases 6, 8 and 10 have landed.

| Date | Backup | Box | Time to a working library | Notes |
|---|---|---|---|---|
| 2026-11-22 | fixture, 300 & 2000 items | this MacBook Air | 0.06 s / 0.12 s restore | `make drill`, procedure only |
| 2026-11-22 | the potato's `pre-update.db` (real schema, real box) | this MacBook Air | 0.05 s backup, 0.05 s restore | see below |
| 2026-11-23 | **the potato's library with 1,476 real saves in it** (912 KiB `.zst`) | **the potato itself** | 0.1 s backup, 0.1 s restore of a 3.7 MB database | the real one, below |

The second row is the real one, with two things it is honest to record:

- **The backup came from `data/backups/pre-update.db`, not a nightly `.zst`**, because no
  `harbor-<UTC>.db.zst` exists on the potato yet: the nightly timer merged on 2026-11-22 and the box has
  not taken the update that ships it. It is still the potato's real database, taken by the update
  watcher, and it restored on a machine that is not the potato.
- **The potato's library is empty** — 0 items, one Space, because nothing has been saved to Harbor yet.
  To have something to lose, 40 saves were added locally to that copy through the API (one then
  soft-deleted) before the backup. Everything after that was real: 39 live items and 1 in Recently
  deleted came back, `harbor export` agreed with the file, and the whole round trip was 0.1 s.

The first genuinely real drill ran on **2026-11-23**, after the Raindrop import put 1,476 saves on the
box: `harbor backup` (911.7 KiB `.zst`, integrity check 272 ms), the container stopped, `restore
--dry-run`, `restore --yes` (integrity check 245 ms, the current database kept as
`pre-restore-20261123T192435Z.db`), container up. The library came back identical — 1,476 items, 268
highlights, 25 Spaces, 1,482 memberships, 7,412 item tags, 2,051 tags — and `/api/status` agreed
5.7 s after the restart. The restore drill on the same box is the one that matters, because it is the
recovery a bad update would actually need.

What is still outstanding is the *other* half of the milestone: a restore **from R2 on a clean box**.
It needs two things, and the credential is only one of them:

1. The Worker's R2 secret (`R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`) — `/presign`
   answers "not configured" without it.
2. **The presign route extended to backup keys.** `presignKeyAllowed` (`worker/src/presign.ts`) allows
   only `incoming|media|archives`, and `knownKey` (`worker/src/queue.ts`) proves a key by looking it up
   in D1's `blobs` — so `backups/harbor-<UTC>.db.zst` is a 400 before the credential is even consulted.
   The bucket is private and the Worker has no read-back route by design, so the download has to be a
   presigned GET: that route needs the prefix and its own key validation, not a D1 row.

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

## The copy on the MacBook Air

`deploy/harbor-rsync.sh` runs on the machine that **receives** the copy and pulls over ssh; the
distroless image has no rsync, so nothing is pushed from inside the container. Run it from cron or a
launchd job:

```bash
./deploy/harbor-rsync.sh                        # ~/Backups/Harbor, backups only
./deploy/harbor-rsync.sh --media --prune        # media too, and mirror deletions
./deploy/harbor-rsync.sh --dry-run
```

- **An unreachable potato exits 0** with a note. A laptop that is asleep is not an error, and a job
  that cries every time it is offline trains people to ignore it.
- **A failed copy exits 1** and says so in `<dest>/.last-rsync.json`. The file answers two questions:
  what happened last night (`outcome`, `detail`, `at`), and when the last *good* copy happened
  (`last_ok_at`, `last_ok_host`, `last_ok_files`, `last_ok_bytes`). A bad night does not overwrite
  the second, so "copied an hour ago, one bad night since" never reads like "not copied in months".
- **`--prune` is opt-in.** By default the destination only ever grows, so a wrong `--from` cannot
  delete the copies you were keeping.

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
