# Updating Harbor

The page to read at 2 a.m. It covers how an update happens, what each signal file
means, what to do when a health check fails, and how to get back.

## The short version

- Harbor runs one digest-pinned image. The potato **never builds**; GitHub
  Actions publishes, GHCR stores, the host pulls.
- Every update is: **snapshot → pin the digest → pull → recreate → health-check
  → roll back if unhealthy**.
- The app can only *ask*. It writes a file with the literal word `latest`; the
  watcher decides which artifact that means. The app can never name an image.
- A rollback is not lossless. Anything saved in-app during the health window is
  lost; remote saves come back with `harbor replay --since <snapshot time>`
  (phase 2).

## Three ways to ask

| Where | What it does |
|---|---|
| Settings → Check for updates | writes `update-signal/check`: resolve `:latest`, write `available.json`, change nothing else |
| Settings → Update now | writes `update-signal/requested` containing `latest` |
| Meridian's Update action, or `sudo systemctl start harbor-update.service` | a bare start with no file: treated as "update now" |

The first two are seen by `harbor-update.path`, which starts
`harbor-update.service`. `harbor-tick.timer` every ten minutes writes a heartbeat
and starts the same service if a signal file was missed — it never updates on its
own.

## The files

All in `~/Harbor/update-signal/`, all written by the app or the watcher, none
editable by hand unless you are deliberately testing:

| File | Written by | Meaning |
|---|---|---|
| `check` | app | please resolve `latest` |
| `requested` | app | please update; contents are exactly `latest` |
| `available.json` | watcher | `{"available":true,"digest":"sha256:…","repo":"…"}` |
| `status.json` | watcher | the last run: `ok`, `failed` or `busy`, with a message and a timestamp |
| `heartbeat` | tick | the watcher is alive; every ten minutes |
| `.lock.d/` | watcher | a run is in progress (a stale one is taken over automatically) |

`HARBOR_IMAGE` in `~/Harbor/.env` is the only line the watcher rewrites, and only
to `<repo>@sha256:<64 hex>`. Every other byte — comments, secrets — is preserved;
`deploy/watcher/test.sh` diffs this.

## What an update actually does

1. **Resolve** `ghcr.io/autumnsgrove/harbor:latest` to a digest, via
   `docker buildx imagetools inspect`. If the registry is unreachable, nothing
   changes.
2. **Refuse a no-op.** If the resolved digest is what is already pinned, the run
   reports `ok` and pulls nothing.
3. **Snapshot**: `docker compose run --rm --no-deps --entrypoint /app/harbor
   harbor snapshot --pre-update` → `data/backups/pre-update.db`. This is
   `VACUUM INTO` + `PRAGMA integrity_check` + fsync + rename, so the snapshot is
   either absent, the previous one, or complete. **If the snapshot fails, the
   update does not happen.**
4. **Pin** the new digest in `.env`.
5. **Pull and recreate** the container. A pull failure re-pins the old digest and
   touches nothing else.
6. **Health** for up to 120 s, polling `http://127.0.0.1:8897/healthz` every
   0.5 s. This is the same endpoint Docker and Meridian probe.

## When it fails

`status.json` says what happened. Read it first:

```bash
cat ~/Harbor/update-signal/status.json
```

**"rolled back to … and healthy"** — the update did not take. The old image is
running, the database is the pre-update snapshot, and remote saves from the window
can be recovered with `harbor replay --since` once phase 2 lands. Nothing to do
except look at why the new image was unhealthy:

```bash
docker compose -f ~/Harbor/docker-compose.yml logs --tail 100 harbor
```

**"rolled back … but still unhealthy"** — the old image is not healthy either.
Restoring the database does not fix a broken volume, a missing `.env` value or a
port conflict. Check, in this order: `docker compose ps`, the logs above,
`curl -v http://127.0.0.1:8897/healthz` (does it answer at all?), and whether
`HARBOR_ALLOWED_HOSTS` contains `127.0.0.1` (the health endpoint still runs the
Host check).

**"snapshot failed; not updating"** — usually a permissions problem on `data/`.
`sudo chown -R 1000:1000 ~/Harbor/data`. The box is still on the old image.

**"could not resolve"** — GHCR is unreachable or the package is private. See
below.

## Restoring by hand

The watcher's rollback copies `data/backups/pre-update.db` back over
`data/harbor.db` while the container is stopped (phase 12 replaces this with
`harbor restore`, which also handles media). To do it yourself:

```bash
cd ~/Harbor
docker compose stop harbor
cp data/backups/pre-update.db data/harbor.db
rm -f data/harbor.db-wal data/harbor.db-shm
chown 1000:1000 data/harbor.db
docker compose up -d
curl -fsS http://127.0.0.1:8897/healthz
```

Keep a copy of the snapshot before you start; it is the only rollback target.

## Publishing, and the GHCR visibility gotcha

`ci.yml` publishes `linux/amd64` + `linux/arm64` on a push to `main` that passes
every test job, tagged `latest` and the short SHA, stamped
`r<commit count>.<short sha>`. A pull request can never reach the publish job.

A **private** package fails the pull with 401/403, which looks exactly like a
broken deploy. Fix it once, at first publish:

- make the package public (the `org.opencontainers.image.source` label links it
  to the repository, so it inherits repository visibility), or
- `docker login ghcr.io` on the potato with a read-only token, and note it in the
  homelab repo.

## Reinstalling or moving the box

```bash
cd ~/Harbor && git pull && sudo deploy/install.sh
```

`install.sh` is idempotent: it reinstalls the units, re-applies the firewall,
pulls, restarts and waits for health. It refuses to run if the port is taken by
something that is not Harbor, if `.env` still has the placeholder login, or if
`HARBOR_ALLOWED_HOSTS` is unset.

The Tailscale mapping is deliberately manual, once:

```bash
sudo tailscale serve --bg --https=8897 http://127.0.0.1:8897
```
