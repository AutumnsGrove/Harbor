# Phase 0 — Foundations: a binary that deploys and updates itself

**Status:** done — deployed and drilled 2026-11-19 · **Design refs:** [§3](../design.md#3-stack-and-deployment-settled-2026-11-03),
[§9](../design.md#9-ui-sveltekit-embedded) (shell only), [§13](../design.md#13-decisions-log)
**Depends on:** nothing · **Gate:** [S7](spikes.md#s7--port-8895-is-free),
[S10](spikes.md#s10--tailscale-serve-headers-and-the-docker-peer-address),
[S11](spikes.md#s11--nova-as-a-dependency) · **Est:** a weekend

## Goal

An empty Harbor that is already *real*: one Go binary with the SvelteKit shell embedded, built by
GitHub Actions into a distroless image and published to GHCR, running on the potato behind
`tailscale serve`, with `/healthz`, the identity lock, the Warbler update watcher, and a pre-update
`VACUUM INTO` snapshot. No library features. The point is that every later phase ships through a pipe
that is already proven, so a phase can never be "done but undeployable".

Nothing built is committed. `web/build/` is a placeholder in git, the Go binary exists only inside
the image, and the potato only pulls. This is the Warbler/Ivy/Polaris packaging shape, not a host
binary.

This is the phase that decides whether the deployment story is true. It is also the phase where the
update and rollback path is exercised **with a deliberately broken image**, because a rollback path
that has never run is not a rollback path.

## Scope

In: repo skeleton, build tooling, CI, image, compose, update watcher, health/version endpoints, the
UI shell with tokens and navigation, the identity lock, the security headers, `contracts/` skeleton.

Out: any SQLite table beyond a health probe, the Worker, saving, Nova, auth beyond the tailnet
identity, Meridian wiring (plan only — see [meridian.md](../meridian.md)).

## Deliverables

```
Makefile                      dev web build test fuzz check docker docker-run deploy clean
Dockerfile                    node:22 frontend stage → golang:1.27 cross-build → gcr distroless;
                              BUILDPLATFORM-pinned build stages, non-root, CGO_ENABLED=0,
                              org.opencontainers.image.source label
.dockerignore                 source-only context: no .git, data/, update-signal/, node_modules,
                              web/build, .svelte-kit, bin, test output
docker-compose.yml            project harbor, 127.0.0.1:8897, ./data, ./update-signal
.env.example                  HARBOR_IMAGE (digest), HARBOR_UID/GID/HOST_PORT/DEPLOYMENT,
                              HARBOR_DATA_DIR, HARBOR_UPDATE_DIR, HARBOR_ALLOWED_LOGIN,
                              HARBOR_ALLOWED_HOSTS, HARBOR_WORKER_URL, HARBOR_WORKER_TOKEN, OPENROUTER_API_KEY
                              (placeholders only; the real .env is mode 600 and never committed)
docker/security-notes.md      egress model (dial-time SSRF guard + host firewall), identity-lock rationale,
                              token rotation drill
deploy/harbor-update.path     watches update-signal/check
deploy/harbor-update.service  oneshot: digest pin (rewrites only the HARBOR_IMAGE line of .env), snapshot,
                              pull, recreate, health, rollback
deploy/harbor-tick.timer      every 10 min heartbeat + missed-signal catch-up
deploy/install.sh             units, .env, dirs, ss -ltn check, first run
deploy/watcher/test.sh        shell tests with fake docker/curl: bare start, check-only, pull failure,
                              unhealthy rollback, snapshot restore, lock contention, timeout paths
deploy/meridian/*.yaml|json   ready-made tile, targets and observe snippets (same as Warbler's);
                              never wired before the service actually runs
deploy/docker-user-firewall.sh  DOCKER-USER rules denying the container RFC1918, loopback, link-local,
                              100.64.0.0/10, fd7a:115c:a1e0::/48 (the tailnet) while allowing the public internet
server/cmd/harbor/main.go     subcommands: serve · healthcheck · snapshot (import/export/backup/restore/replay
                              arrive with their phases); config → store → httpapi → graceful shutdown
server/internal/build/        version stamp, env parsing (HARBOR_*), deploy mode
server/internal/httpapi/      router, middleware, static handler, /healthz, /api/version
server/internal/httpapi/update.go   check/available/start/status, signal files, /api/busy
server/internal/store/        open DB, WAL pragmas, migrations runner (empty migration set)
server/internal/store/snapshot.go   VACUUM INTO temp → fsync → rename + integrity check; the watcher calls
                              it as `harbor snapshot --pre-update`, and phase 12's backup reuses it
web/                          SvelteKit shell: app.css ← tokens.css, nav, empty home, settings stub
web/build/.gitkeep            the only tracked file under build/; `make web` fills it
web/embed.go                  //go:embed all:build
docs/update.md                the watcher flow, signal files, bare-start/Meridian rule, digest pin,
                              rollback and GHCR visibility — the page a tired operator reads
contracts/                    README + empty schema stubs so the directory exists from day one
.github/workflows/ci.yml      gofmt, vet, test -race, fuzz seed corpus, pnpm check/test/build,
                              packaging checks; on green main, a gated publish job builds
                              linux/amd64 + linux/arm64 and pushes latest + short SHA
```

## Tests first

Written before the code, in this order:

1. `build.Version()` — `r<commit-count>.<short-sha>` from ldflags, and a readable fallback when
   unstamped (`dev`). Property: output always matches `^[A-Za-z0-9._-]{1,64}$` (it goes in a header
   and a JSON field).
2. `build.Config` — env parsing with defaults and rejections (bad port, missing data dir, unknown
   deployment mode). Table-driven; fuzz `HARBOR_PORT` and `HARBOR_UID` strings for "never panics,
   either parses or errors".
3. `httpapi` `/healthz` — exactly two JSON fields, no DB access, no blocking call; responds <10 ms
   with the DB closed and the queue loop absent (start the server with no store).
4. `httpapi` identity lock — the rule is header-based, because `tailscale serve` and Docker both make
   "loopback" meaningless (S10): every request needs `Tailscale-User-Login` equal to
   `HARBOR_ALLOWED_LOGIN`; a missing or wrong login is 403; `Host` not in the allowlist is 421/403.
   Exactly two exemptions — `GET /healthz` and `GET /api/status` — and only when the identity header
   is *absent*. A request to `/api/items` with no header is refused even from 127.0.0.1; a request
   to `/healthz` carrying a *wrong* login is also refused. A dev-only `HARBOR_DEV_NO_AUTH=1` bypass
   exists and refuses to start when `HARBOR_DEPLOYMENT=docker`.
5. `httpapi` update endpoints — `POST /api/update/check` writes `update-signal/check` and nothing
   else; `POST /api/update/start` writes the literal `latest` and never an image name; a traversal
   attempt in a signal-file path is impossible (paths are constants).
6. `store` — migrations runner applies zero migrations, sets `journal_mode=WAL`, and refuses to open
   a directory it cannot write.
7. `store.Snapshot` — produces a database that passes `PRAGMA integrity_check`, via temp → rename (no
   partial file at the final name if interrupted), and succeeds against a DB with an open writer.
   `harbor healthcheck` exits 0 only on a 200 from `/healthz` and non-zero otherwise (this is the
   Docker `HEALTHCHECK`; distroless has no curl).
8. Watcher `.env` handling (shell test with a fixture `.env`) — only the `HARBOR_IMAGE=` line changes;
   every other line, including secrets, survives byte-for-byte; a digest not matching
   `<repo>@sha256:<64 hex>` is refused.
9. `web` — `svelte-check` clean; one vitest test that the API client maps a 403 to a typed error, so
   the client exists and is wired.
10. **Packaging** — `git ls-files web/build` returns exactly `web/build/.gitkeep`; `.dockerignore`
    excludes `web/build`, `node_modules`, `data/` and `update-signal/`; a clean checkout with no
    `web/build` can run `make build` (it builds the frontend first) and `docker build` (the
    frontend stage fills the embed directory). The built image contains no Node, npm or pnpm, and
    runs as uid 1000.
11. **Publish workflow** — the publish job is reachable only on a push to `main` (or manual
    dispatch), `needs` every test job, and publishes both `latest` and the short SHA. A pull request
    cannot reach the registry job. The version build arg is `r<commit count>.<short sha>`.

## Tasks

**Tooling**

- [x] `go mod init github.com/AutumnsGrove/Harbor`; Go 1.27 pinned in `go.mod`. Nova is pinned to
      the S11-tested commit (`ef460da`, `v0.5.1-0.20261116000603-ef460da11cfc`) in phase 3, when the
      first import exists: an unused `require` is dropped by `go mod tidy`, so pinning it in phase 0
      would only leave a line that the next tidy deletes. S11 needs no build secret either way.
- [ ] Makefile targets; `make check` runs everything CI runs. `build` depends on `web`; `clean`
      removes `bin/`, `web/build/*` and restores `web/build/.gitkeep`.
- [x] SvelteKit scaffold (static adapter, no SSR), import `tokens.css` verbatim as `src/app.css`,
      set the Schibsted Grotesk face (bundled woff2, no external fetch) and the dark-only base.
- [x] `web/build/.gitkeep` plus `.gitignore` rules (`web/build/*`, `!web/build/.gitkeep`); no build
      output is ever tracked.
- [x] `web/embed.go` with `//go:embed all:build`; static handler serves `index.html` for unknown
      paths (SPA) but **not** for `/api/*`.
- [x] `contracts/README.md` stating the rule: a field change lands with a fixture.

**Server**

- [x] Config from env with explicit defaults; fail fast and loudly on a bad value.
- [x] Router + middleware order: recover → request id → structured JSON log → security headers
      (strict CSP, no CORS) → `Host` check → identity lock → handler.
- [x] `/healthz` (static, two fields), `/api/version`, `/api/busy`, a stub `/api/status` (counts of
      zero until later phases fill it), and the `harbor healthcheck` subcommand wired into the
      compose `healthcheck:`.
- [x] Update endpoints writing only `check` / `requested`; read `available.json` and
      `status.json` for the UI. The app never names an image.
- [x] Structured JSON logs to stdout with a request id and duration.

**Container and deploy**

- [x] Dockerfile, following Warbler/Ivy/Polaris: a `node:22-bookworm` web stage pinned to
      `--platform=$BUILDPLATFORM` (`corepack` + `pnpm@10.32.1`, `pnpm install --frozen-lockfile`,
      `pnpm build`); a `golang:1.27-alpine` stage pinned the same way, copying the web build into
      `web/build`, `CGO_ENABLED=0 GOOS/GOARCH` and `-ldflags "-s -w -X …Version=…"`; a
      `gcr.io/distroless/static-debian12:nonroot` runtime with the CA certificates, `USER 1000`,
      `EXPOSE 8080`, `HEALTHCHECK CMD ["/app/harbor","healthcheck"]`, and
      `org.opencontainers.image.source`.
- [x] `.dockerignore` (source-only context) and a CI step that asserts `web/build` is not tracked
      except `.gitkeep`.
- [x] `docs/update.md`, modeled on Warbler's: three entry points (Settings, Meridian bare start, by
      hand), the signal files, the digest-only rule, the 120 s health window, snapshot restore, and
      the GHCR visibility gotcha. Keep it short enough to follow at 2 a.m.
- [x] `deploy/meridian/` snippets for the tile, `/etc/meridian/targets.json` and
      `/etc/meridian/observe.json`; `docs/meridian.md` remains the explanation, the snippets are
      what get copied on deploy day.
- [x] Compose with `read_only`, `cap_drop: ALL`, `no-new-privileges`, tmpfs `/tmp`, `./data:/data`,
      `./update-signal:/update-signal`, `127.0.0.1:8897:8080`, `env_file: [.env]`, and the
      `healthcheck:` from design §3.
- [x] Units + `install.sh`; the watcher accepts only `<repo>@sha256:<64 hex>` and refuses anything
      else, rewrites only the `HARBOR_IMAGE=` line of `.env`, and takes the pre-update snapshot with
      `docker compose exec harbor /app/harbor snapshot --pre-update`.
- [x] The watcher handles **both** a signal file and a bare `systemctl start` (Meridian's Update
      action): no file means "resolve `latest`, pin, snapshot, pull, recreate, health-check,
      rollback". Write `deploy/watcher/test.sh` before the script, covering both paths and every
      failure exit.
- [x] `deploy/docker-user-firewall.sh` and its install step: the container can reach the public
      internet but not private, loopback, link-local or tailnet ranges (verify by `docker exec` into
      a throwaway container of the same network: `curl 100.x.x.x` fails, `curl example.com` works).
- [x] CI: a `publish` job in `ci.yml` (the Warbler shape), `needs` every test job, only on a push
      to `main`; `workflow_dispatch` may publish a `broken-test` tag without touching `latest`.
      Build `linux/amd64` + `linux/arm64`, tags `latest` + short SHA, GHA layer cache, version build
      arg. Tests run with `CGO_ENABLED=1` (the race detector needs cgo) while the image build stays
      `CGO_ENABLED=0`. S11 already proved a clean `docker build` can fetch Nova with no secret;
      keep that check in CI.
- [x] After the first publish, make the GHCR package public (or log the potato in) and verify an
      anonymous `docker pull` by digest. The source label links the package to the repo. Verified
      2026-11-19: an anonymous token request and `GET /v2/autumnsgrove/harbor/manifests/latest` both
      return 200, and the potato pulls with no `~/.docker/config.json` entry.
- [x] `security-notes.md`: the egress model (a hostname allowlist is impossible because the app
      fetches arbitrary pages — dial-time SSRF guard in phase 3 plus the host firewall rule here),
      the header-based identity lock and the S10 evidence for it, and the token rotation drill.

**Wire-up**

- [x] Deploy to the potato behind `tailscale serve`; confirm the click-through URL from
      [meridian.md](../meridian.md). Live at `https://lepotato.taild95077.ts.net:8897` since
      2026-11-18.
- [x] File the Meridian issue at planning time; do **not** add tile/vital configs yet. Filed as
      [#1](https://github.com/AutumnsGrove/Harbor/issues/1) (Meridian side:
      [Meridian#15](https://github.com/AutumnsGrove/Meridian/issues/15)). The tile/vital snippets
      are not wired — that waits until the Status page has numbers.
- [x] Settings shell shows version and the update check/apply flow, disabled where the backend is
      not yet there. Exercised end to end on 2026-11-19 through `POST /api/update/check` and
      `POST /api/update/start` (the browser clicks the same two calls).

## Exit criteria

Automated: `make check` green; `/healthz` under 10 ms with no DB; identity lock rejects both a missing
and a wrong login; `git ls-files web/build` shows only `.gitkeep`; a clean `docker build` works; the
image is non-root, has no Node runtime, and its rootfs is read-only (inspect the running container).

Demo, on the potato, all of it:

1. `docker compose up -d` → `curl localhost:8897/healthz` returns `{"status":"ok","version":"r…"}`;
   the tailnet URL loads the shell in Safari, pulled from GHCR rather than built locally.
2. Settings → Check finds a newer digest; Update applies it; the version string changes.
3. **Rollback drill:** push a deliberately broken image tag, apply it, watch the watcher restore the
   previous digest and the app come back healthy. Record the timings; if it takes >120 s, the health
   window is wrong.
4. `data/backups/pre-update.db` exists after the update and opens in `sqlite3` with an integrity
   check passing.
5. Port 8897 confirmed free before install (S7 verdict recorded; 8895 is Pixelsort), the
   identity-lock design matches what S10 measured, and S11 shows CI and Docker can fetch Nova.
6. A container restart (and a potato reboot) comes back without manual intervention.

## Deploy record (2026-11-19)

Run on the real potato (`lepotato`, aarch64), from the real doors. The first install was
2026-11-18; this is the update, the rollback, the container restart and the reboot.

| Step | Result | Wall clock |
|---|---|---|
| `POST /api/update/check` (Settings → Check) | `available.json` with `sha256:f0a3ca06…` | 4 s |
| `POST /api/update/start` (Settings → Update) | `r52.13940dc` → **`r56.4c93aa8`**, digest pinned | 43 s |
| Pre-update snapshot | `data/backups/pre-update.db`, `PRAGMA integrity_check` = ok | 53 ms |
| Rollback drill | bad image pulled, unhealthy, rolled back, `r56.4c93aa8` healthy | **176 s** |
| Container restart | healthy again immediately, `restart: unless-stopped` | < 1 s |
| Potato reboot | reboot issued 18:10:12Z, container up 18:11:22Z, `/healthz` ok, all three units active | ~45 s to serving |
| Tailnet | `https://lepotato.taild95077.ts.net:8897` serves `/healthz` and the shell | — |

The reboot also exposed that the box's own post-boot check did not cover Harbor at all (it had
never been added since the 2026-11-18 install). Fixed the same day in the homelab repo
(`homelab#6`): container health plus `http://localhost:8897/healthz` on every boot. That was a real
gap — the first reboot after install would have emailed a green summary without having asked Harbor
anything.

**The drill, and how to repeat it.** The watcher always resolves `<repo>:latest`; it never applies
a digest someone pinned by hand. So a `broken-test` tag alone cannot drill rollback —
`systemctl start` would just see `latest` still pointing at the good image and do a no-op update.
What works is moving `latest` itself to the broken image, which is also the real failure mode (a
commit that passes the tests but cannot serve):

```bash
# 0. once per machine: a GHCR credential that can push (write:packages)
docker login ghcr.io -u AutumnsGrove

# 1. a broken image: starts, never healthy (no /app/harbor, so the healthcheck exits 127)
mkdir -p /tmp/drill
printf 'FROM busybox:1.36\nCMD ["sleep","infinity"]\n' >/tmp/drill/Dockerfile
docker buildx build --platform linux/amd64,linux/arm64 --push \
  -t ghcr.io/autumnsgrove/harbor:broken-test /tmp/drill
broken=$(docker buildx imagetools inspect ghcr.io/autumnsgrove/harbor:broken-test \
  --format '{{.Manifest.Digest}}')
good=$(docker buildx imagetools inspect ghcr.io/autumnsgrove/harbor:latest \
  --format '{{.Manifest.Digest}}')

# 2. move latest, then let the watcher take it
docker buildx imagetools create -t ghcr.io/autumnsgrove/harbor:latest \
  ghcr.io/autumnsgrove/harbor@$broken
ssh potato-remote 'sudo systemctl start harbor-update.service'   # exits 1 after a rollback

# 3. put latest back — this is the step that matters, or every later update retries the bad image
docker buildx imagetools create -t ghcr.io/autumnsgrove/harbor:latest \
  ghcr.io/autumnsgrove/harbor@$good
```

Timings from the drill: resolve 19 s (`buildx imagetools` on the potato) → snapshot 4 s → pull 7 s
→ recreate 4 s → health wait **120 s** (the full window; the container was `Up` but `/healthz` never
answered) → restore 13 s → up 4 s. Total 176 s, of which 137 s is the broken version serving
nothing. The 120 s health window is doing its job; the number to quote to a human is "about three
minutes of outage, then the old version is back". (The named steps sum to 171 s; the rest is the
shell's own overhead between them.)

Two wrinkles worth knowing:

- `systemctl start harbor-update.service` exits **non-zero** after a successful rollback (the oneshot
  failed its job). Meridian's control target therefore reports a failure even though the box
  recovered — read `update-signal/status.json`, not the exit code.
- A rollback does not change `latest`. If the bad image is what CI published, every later Update
  retries it and costs another three minutes. Fixing that means reverting on `main` and letting the
  publish job move `latest` back, or retagging `latest` as in step 3.

The `harbor:broken-test` tag is left in the package on purpose: it is the drill artifact, and
re-tagging it is step 2 above.

## Risks

- **Distroless + `read_only` + SQLite.** WAL needs write access to the data dir only; verify no
  writes to `/` (set `TMPDIR=/tmp`). A silent failure here looks like "database is locked".
- **Update watcher staleness.** Test the tick timer's catch-up; a missed signal must not leave the
  app waiting forever.
- **CSP with an embedded SPA.** Built and settled: SvelteKit emits one inline bootstrap script per
  page (`__sveltekit_…` plus a dynamic import). `web/scripts/csp.mjs` hashes it into
  `web/build/csp.json` and the Go server adds exactly those `sha256-` sources to `script-src`,
  validating each one before it reaches a header. Do not "temporarily" allow `unsafe-inline`.
- **The identity header is easy to trust wrongly.** It is only trustworthy because `tailscale serve`
  sets (and overwrites) it and the published port is bound to 127.0.0.1. Inside Docker the peer
  address is the bridge gateway, so you cannot test "is it loopback" — only "is the header present
  and right", with `/healthz` and `/api/status` as the sole header-less exemptions. If S10 shows a
  tailnet client can forge the header, the lock must also verify with `tailscale whois`.
- **A rollback window loses in-app saves.** Restoring the pre-update snapshot drops anything saved in
  the app during the health window; remote saves come back with `harbor replay` (phase 2). Say so in
  the runbook; do not pretend the rollback is lossless.
- **A private GHCR package looks like a broken deploy.** The first publish creates the package; make
  it public or log the potato in before the watcher pulls. The source label is what links it to the
  repository and lets it inherit visibility.
- **A stale committed frontend build.** One tracked `web/build/.gitkeep` and no other build files;
  the Docker frontend stage and `make web` are the only producers. A CI check enforces it.

## Build-time decisions to record

- The exact `-ldflags` wiring for commit count/sha (build arg vs `.git` in the builder).
- Health window (120 s) and poll interval (0.5 s) for the watcher.
- Whether `install.sh` creates the Tailscale serve mapping or just documents it (lean: document, do
  it by hand once).
- Publish job in `ci.yml` (gated by tests) vs a separate `docker-publish.yml` (the Ivy/Polaris
  shape). Chosen: `ci.yml`, because a failing test must never publish an image.
- Base image pins (`node:22-bookworm`, `golang:1.27-alpine`, `distroless/static-debian12:nonroot`)
  and whether a `vX.Y.Z` tag is added later. Not needed for v1; `latest` + short SHA is enough.

## Review carry-over (2026-11-18)

The first pi-review pass on the phase-0 stack found four things worth keeping,
fixed in place, and four that are deliberately deferred. Fixes: the healthcheck
host is required in docker mode (a 421 there would fail a healthy image), the
watcher reconciles a half-finished run and no longer advertises an empty digest
as available, the watcher releases its lock on a signal and exits, and
`make packaging` builds the frontend so the off-host rule cannot be skipped
locally. Deferred:

- **`Snapshot`'s atomic property is untested.** A cancelled context returns
  before the temp file exists, so deleting `dest` early would still pass the
  suite. A `verify` seam injected in the test would cover it; it lands with the
  backup work in phase 12a, which is where the real rollback drill runs.
- **Two migrators are not expected.** A server process and `harbor snapshot`
  both open the data directory; if a migration is pending, the loser aborts with
  a SQL error. Harmless with an empty migration set, real from phase 2. Decide
  then: a `PRAGMA busy_timeout` retry around the migration transaction, or a
  lock file.
- **The egress firewall covers forwarded traffic only.** `DOCKER-USER` is
  traversed for forwarded packets; container→host-local delivery uses `INPUT`.
  What Harbor can reach on the host itself is therefore covered by the dial-time
  SSRF guard (phase 3), not by this rule set. Re-check the DNS resolver path on
  the potato (`/etc/resolv.conf`) before enabling the unit: a LAN resolver would
  be rejected by these rules and break every fetch while `/healthz` stays green.
- **The Settings update flow needs a second look** in the browser once the
  watcher runs on a real host (PR #8's note): the check→available polling window
  and the "not built yet" copy are the parts to revisit. `web/package.json` also
  wants a `packageManager` pin so a newer pnpm's `minimumReleaseAge` policy
  cannot reject the lockfile on a fresh machine.
