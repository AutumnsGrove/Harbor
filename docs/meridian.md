# Meridian and homelab integration

Harbor is **not deployed yet**. Nothing in this document should be wired up before the box actually
runs it — a probe for a service that isn't installed is a permanently red tile, and a red tile
teaches you to ignore the dashboard.

This follows the homelab repo's own "Adding New Services" rule: reserve the port, write the page,
file the Meridian issue at **planning** time, then do the config edits on deploy day. The homelab
repo itself is only touched once Harbor is ready (see the checklist at the end).

Tracked by [Harbor #1](https://github.com/AutumnsGrove/Harbor/issues/1). The Meridian-side work is
[AutumnsGrove/Meridian#15](https://github.com/AutumnsGrove/Meridian/issues/15). Related:
[design.md](design.md) §3 for the update watcher, and Warbler's `docs/update.md` for the pattern
this copies.

---

## Reserved facts

| Fact | Value |
|---|---|
| Port | **8897** (S7, 2026-11-18: 8895 is Pixelsort, 8893 Sepia, 8894 Coffer reserved, 8896 Outpost) |
| Click-through URL | `https://lepotato.taild95077.ts.net:8897` |
| Probe URL | `http://localhost:8897/healthz` |
| Health shape | `{"status":"ok","version":"r<commit-count>.<short-sha>"}` — two fields, nothing personal |
| Compose | project `harbor` at `~/Harbor`, service `harbor`, image pinned by digest in `~/Harbor/.env` |
| Image | `ghcr.io/autumnsgrove/harbor` (amd64 + arm64, built and published by the gated `ci.yml` publish job) |
| Data | `~/Harbor/data/` — `harbor.db` (+ WAL), `media/`, `backups/` |
| Update unit | `harbor-update.service` (host watcher, same shape as `warbler-update.service`) |
| Backup | nightly `VACUUM INTO` → zstd → R2 (30 days) + rsync to the MacBook Air; media mirrors continuously |
| Criticality | **low** — a personal library, not infrastructure |

Before first install, confirm the port is free on the potato:

```bash
ssh potato-remote 'ss -ltn | grep -E ":(8893|8894|8895|8897)\b" || echo "8897 free"'
```

---

## What Meridian requires from Harbor

Meridian needs no new code for Harbor — only the config additions below. The contract is the same
one that Warbler, Polaris and Ivy follow:

- **A cheap, header-free health probe.** `GET http://localhost:8897/healthz` returns
  `{"status":"ok","version":"r…"}` and never waits on SQLite, the poll loop or a model. It is exempt
  from the identity lock only when the `Tailscale-User-Login` header is absent, which is exactly a
  host-local Meridian probe. The click-through URL is the `https://…ts.net:8897` one, because a
  browser must go through `tailscale serve`.
- **A compose target at the repo root.** `docker-compose.yml` lives at `~/Harbor`, the service is
  named `harbor`, and the image is pinned by digest in `~/Harbor/.env`. Meridian runs
  `docker compose` in that directory; it never builds.
- **A unit-shaped update.** Meridian's Update action starts `harbor-update.service` **with no signal
  file**. The watcher therefore treats a bare start as "resolve `latest`, pin the digest, snapshot,
  pull, recreate, health-check, roll back on failure" — the Warbler rule. A Settings request writes
  `update-signal/requested` with the literal `latest`; both paths run the same script.
- **An observable updater.** Add `harbor-update.path` to `/etc/meridian/observe.json`, so Meridian
  can tell whether the watcher is actually installed.
- **A pullable image.** The GHCR package must be public, or the potato must be logged in to GHCR.
  The `org.opencontainers.image.source` label links the package to the repository.

The publish job is gated by the test jobs and runs only on `main`; a pull request never produces an
image. That means the thing Meridian updates to is always a tested commit.

---

## What `/api/status` must expose

Meridian reads one number for the tile's vital; the app's own Settings → Status page reads the rest.
Counts only, no titles or URLs:

```json
{
  "items_total": 1234,
  "saved_this_week": 12,
  "queue_depth": 0,
  "mirror_pending": 0,
  "last_backup": "2026-11-03T03:00:00Z",
  "last_link_check": "2026-11-03T04:00:00Z",
  "disk_used_pct": 41
}
```

`/api/status` is exempt from the identity lock only when the Tailscale identity header is absent —
i.e. a host-local caller such as Meridian probing `localhost`; counts only, no content (design §3
explains why "loopback-only" cannot be tested inside Docker). It is not part of the public surface.
`/healthz` stays a cheap static answer and must never wait on SQLite, the poll loop, or a model
call — the tile would flap during normal work.

The vital is **`saved_this_week`**: it answers "am I actually using it", which a build stamp cannot.
If that proves noisy, fall back to `items_total` or the build stamp.

---

## 1. Tile — `~/Meridian/config.yaml`

```yaml
  - id: harbor
    name: Harbor
    section: everyday
    icon: library          # Lucide name; adjust once the real mark exists
    description: saved links, notes, images
    url: https://lepotato.taild95077.ts.net:8897
    probes:
      health:
        kind: http_json
        url: http://localhost:8897/healthz
        path: status
        equals: ok
      vitals:
        - kind: json_field
          label: saved this week
          url: http://localhost:8897/api/status
          path: saved_this_week
    control:
      target: harbor
      actions: [restart, stop, start, update]
```

Meridian watches `config.yaml`, so the tile appears within a second. Do **not** reload it with
`docker kill -s HUP` — Docker records any kill-API signal as a manual stop (Meridian decision #100).

## 2. Allowlist — `/etc/meridian/targets.json` (root-owned)

```json
"harbor": {
  "kind": "compose",
  "dir": "/home/autumn/Harbor",
  "service": "harbor",
  "actions": ["start", "stop", "restart", "update"],
  "criticality": "low",
  "update": { "kind": "unit", "unit": "harbor-update.service" }
}
```

Then run Meridian's allowlist doctor:

```bash
ssh potato-remote 'docker exec -i meridian /meridian doctor --allowlist -'
```

## 3. Observed unit — `/etc/meridian/observe.json`

Add `harbor-update.path` to the existing list (do not replace it):

```json
{"units": ["her-go.service", "meridian-control.path", "harbor-update.path"]}
```

---

## Deploy-day sequence

1. **Install the checkout and image.** `cd ~/Harbor && deploy/install.sh`, which pulls the published
   image from GHCR (it does not build), then verify `curl http://localhost:8897/healthz` from the
   potato.
2. **Expose it on the tailnet.** `sudo tailscale serve --bg --https=8897 http://127.0.0.1:8897`,
   then confirm the ts.net URL loads from the phone.
3. **Install the update watcher.** `harbor-update.path`, `harbor-update.service`, `harbor-tick.timer`;
   press Update from Settings once, then start the same unit bare (as Meridian does) and confirm the
   audit/result file. Both paths must work.
4. **Backups.** Nightly snapshot + R2 upload + MacBook rsync; run a restore drill into a scratch
   directory before calling it done.
5. **Meridian.** Add the tile, allowlist target, and observed unit above; one restart pressed from
   `/control` and confirmed in `~/Meridian/control/outbox/audit.jsonl`.
6. **Homelab repo — only now** (see the next section).

---

## Homelab repo changes (when ready, not before)

The homelab repo's rule is: the same change as the install, never ahead of it.

- [ ] `README.md` — service entry (URL, port, compose project, data dir, update command, backup line),
      a Quick Access row, a port-map row for 8897 Harbor (8895 stays Pixelsort), a backup-table row,
      and a security note covering the outbound calls (OpenRouter + the Cloudflare Worker) and the
      accepted no-extra-encryption backup decision.
- [ ] `docs/harbor.md` — a live service page modeled on `docs/warbler.md`/`docs/coffer.md`: what
      actually runs, the update path, storage layout, backup/restore, troubleshooting.
- [ ] `maintenance-setup/service-healthcheck.sh` — same shape as Warbler:

  ```bash
  check_docker_health "Harbor" "harbor" "harbor"
  check_http "Harbor" "http://localhost:8897/healthz"
  ```

- [ ] Backup job in the same change as the data, not after: `~/Harbor/data` holds the only
      irreplaceable database and the local media.
- [ ] Restore drill actually run (database + media; a database without media is not a restore).
- [ ] `README.md`'s service/check counts and the backup table updated so they match `docker ps`.
- [ ] `CLAUDE.md` (homelab repo) — any AI-assistant instructions that changed.

---

## Before wiring anything, confirm

- [ ] Port 8897 is free on the potato (8895 is Pixelsort)
- [ ] `/healthz` answers 200 in milliseconds from the host, with the DB and poll loop cold
- [ ] `/api/status` returns an integer `saved_this_week` (verified with `curl`)
- [ ] The GHCR package is public, or the potato is logged in to GHCR
- [ ] `harbor-update.service` can pull by digest and roll back on a deliberately broken image
- [ ] A restore from R2 + the media mirror works
- [ ] Meridian's tile shows green, the vital shows a number, and a restart appears in the audit log
- [ ] The homelab health check and README match reality in the same change

## Open questions for deploy day

- **Vital:** `saved_this_week` is the lean. If it turns into a vanity number, switch to
  `items_total` or just the build stamp.
- **Meridian search row:** should "Search Harbor" sit next to *Search* and *Ask Polaris*? It would
  deep-link the library's search with the query. Two-line config change if wanted; not a blocker.
- **Criticality:** `low` is right for a personal library. Revisit only if other services start
  depending on it.
