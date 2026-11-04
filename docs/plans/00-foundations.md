# Phase 0 — Foundations: a binary that deploys and updates itself

**Status:** planned · **Design refs:** [§3](../design.md#3-stack-and-deployment-settled-2026-11-03),
[§9](../design.md#9-ui-sveltekit-embedded) (shell only), [§13](../design.md#13-decisions-log)
**Depends on:** nothing · **Gate:** [S7](spikes.md#s7--port-8895-is-free),
[S10](spikes.md#s10--tailscale-serve-headers-and-the-docker-peer-address),
[S11](spikes.md#s11--nova-as-a-dependency) · **Est:** a weekend

## Goal

An empty Harbor that is already *real*: one Go binary with the SvelteKit shell embedded, in a
distroless image, publishing to GHCR, running on the potato behind `tailscale serve`, with `/healthz`,
the identity lock, the Warbler update watcher, and a pre-update `VACUUM INTO` snapshot. No library
features. The point is that every later phase ships through a pipe that is already proven, so a phase
can never be "done but undeployable".

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
Dockerfile                    multi-stage → gcr distroless, non-root, CGO_ENABLED=0
docker-compose.yml            project harbor, 127.0.0.1:8895, ./data, ./update-signal
.env.example                  HARBOR_IMAGE (digest), HARBOR_UID/GID/PORT/DEPLOYMENT, HARBOR_ALLOWED_LOGIN,
                              HARBOR_ALLOWED_HOSTS, HARBOR_WORKER_URL, HARBOR_WORKER_TOKEN, OPENROUTER_API_KEY
                              (placeholders only; the real .env is mode 600 and never committed)
docker/security-notes.md      egress model (dial-time SSRF guard + host firewall), identity-lock rationale,
                              token rotation drill
deploy/harbor-update.path     watches update-signal/check
deploy/harbor-update.service  oneshot: digest pin (rewrites only the HARBOR_IMAGE line of .env), snapshot,
                              pull, recreate, health, rollback
deploy/harbor-tick.timer      every 10 min heartbeat + missed-signal catch-up
deploy/install.sh             units, .env, dirs, ss -ltn check, first run
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
web/embed.go                  //go:embed all:build
contracts/                    README + empty schema stubs so the directory exists from day one
.github/workflows/ci.yml      gofmt, vet, test -race, fuzz seed corpus, pnpm check/test/build
.github/workflows/release.yml multi-arch build + push on green main
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

## Tasks

**Tooling**

- [ ] `go mod init github.com/AutumnsGrove/Harbor`; Go version pinned in `go.mod`.
- [ ] Makefile targets; `make check` runs everything CI runs.
- [ ] SvelteKit scaffold (static adapter, no SSR), import `tokens.css` verbatim as `src/app.css`,
      set the Schibsted Grotesk face and the dark-only base.
- [ ] `web/embed.go` with `//go:embed all:build`; static handler serves `index.html` for unknown
      paths (SPA) but **not** for `/api/*`.
- [ ] `contracts/README.md` stating the rule: a field change lands with a fixture.

**Server**

- [ ] Config from env with explicit defaults; fail fast and loudly on a bad value.
- [ ] Router + middleware order: recover → request id → structured JSON log → security headers
      (strict CSP, no CORS) → `Host` check → identity lock → handler.
- [ ] `/healthz` (static, two fields), `/api/version`, `/api/busy`, a stub `/api/status` (counts of
      zero until later phases fill it), and the `harbor healthcheck` subcommand wired into the
      compose `healthcheck:`.
- [ ] Update endpoints writing only `check` / `requested`; read `available.json` and
      `status.json` for the UI. The app never names an image.
- [ ] Structured JSON logs to stdout with a request id and duration.

**Container and deploy**

- [ ] Dockerfile: build the web app, build the Go binary with `-ldflags "-X …Version=…"`, copy into
      distroless, `USER 1000`, `ENTRYPOINT ["/harbor"]`.
- [ ] Compose with `read_only`, `cap_drop: ALL`, `no-new-privileges`, tmpfs `/tmp`, `./data:/data`,
      `./update-signal:/update-signal`, `127.0.0.1:8895:8080`, `env_file: [.env]`, and the
      `healthcheck:` from design §3.
- [ ] Units + `install.sh`; the watcher accepts only `<repo>@sha256:<64 hex>` and refuses anything
      else, rewrites only the `HARBOR_IMAGE=` line of `.env`, and takes the pre-update snapshot with
      `docker compose exec harbor /harbor snapshot --pre-update`.
- [ ] `deploy/docker-user-firewall.sh` and its install step: the container can reach the public
      internet but not private, loopback, link-local or tailnet ranges (verify by `docker exec` into
      a throwaway container of the same network: `curl 100.x.x.x` fails, `curl example.com` works).
- [ ] CI: multi-arch image, tags `latest` + short SHA, provenance attestation if trivial. Tests run
      with `CGO_ENABLED=1` (the race detector needs cgo) while the image build stays
      `CGO_ENABLED=0`. Per S11, prove CI and `docker build` can fetch Nova (build secret, not an
      image layer) even though nothing imports it until phase 3.
- [ ] `security-notes.md`: the egress model (a hostname allowlist is impossible because the app
      fetches arbitrary pages — dial-time SSRF guard in phase 3 plus the host firewall rule here),
      the header-based identity lock and the S10 evidence for it, and the token rotation drill.

**Wire-up**

- [ ] Deploy to the potato behind `tailscale serve`; confirm the click-through URL from
      [meridian.md](../meridian.md).
- [ ] File the Meridian issue at planning time; do **not** add tile/vital configs yet.
- [ ] Settings shell shows version and the update check/apply flow, disabled where the backend is
      not yet there.

## Exit criteria

Automated: `make check` green; `/healthz` under 10 ms with no DB; identity lock rejects both a missing
and a wrong login; the image is non-root and its rootfs read-only (inspect the running container).

Demo, on the potato, all of it:

1. `docker compose up -d` → `curl localhost:8895/healthz` returns `{"status":"ok","version":"r…"}`;
   the tailnet URL loads the shell in Safari.
2. Settings → Check finds a newer digest; Update applies it; the version string changes.
3. **Rollback drill:** push a deliberately broken image tag, apply it, watch the watcher restore the
   previous digest and the app come back healthy. Record the timings; if it takes >120 s, the health
   window is wrong.
4. `data/backups/pre-update.db` exists after the update and opens in `sqlite3` with an integrity
   check passing.
5. Port 8895 confirmed free before install (S7 verdict recorded), the identity-lock design matches
   what S10 measured, and S11 shows CI and Docker can fetch Nova.
6. A container restart (and a potato reboot) comes back without manual intervention.

## Risks

- **Distroless + `read_only` + SQLite.** WAL needs write access to the data dir only; verify no
  writes to `/` (set `TMPDIR=/tmp`). A silent failure here looks like "database is locked".
- **Update watcher staleness.** Test the tick timer's catch-up; a missed signal must not leave the
  app waiting forever.
- **CSP with an embedded SPA.** Keep it strict; Svelte needs no inline script if the build is
  configured for it. Do not "temporarily" allow `unsafe-inline`.
- **The identity header is easy to trust wrongly.** It is only trustworthy because `tailscale serve`
  sets (and overwrites) it and the published port is bound to 127.0.0.1. Inside Docker the peer
  address is the bridge gateway, so you cannot test "is it loopback" — only "is the header present
  and right", with `/healthz` and `/api/status` as the sole header-less exemptions. If S10 shows a
  tailnet client can forge the header, the lock must also verify with `tailscale whois`.
- **A rollback window loses in-app saves.** Restoring the pre-update snapshot drops anything saved in
  the app during the health window; remote saves come back with `harbor replay` (phase 2). Say so in
  the runbook; do not pretend the rollback is lossless.

## Build-time decisions to record

- The exact `-ldflags` wiring for commit count/sha (build arg vs `.git` in the builder).
- Health window (120 s) and poll interval (0.5 s) for the watcher.
- Whether `install.sh` creates the Tailscale serve mapping or just documents it (lean: document, do
  it by hand once).
