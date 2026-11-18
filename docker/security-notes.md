# Security notes

Harbor is one process on a tailnet-only box, holding the one library Autumn
actually cares about. This page is the reasoning behind the four layers, so a
future change can be judged against them instead of guessed at.

## The layers

1. **The tailnet.** The container publishes only on `127.0.0.1`; `tailscale serve`
   is the only route in. There is no public listener, no reverse proxy and no
   read-only token that exposes content.
2. **The `Host` allowlist.** `HARBOR_ALLOWED_HOSTS` is hostnames only. A request
   whose `Host` is not on the list gets `421`. This is the DNS-rebinding guard;
   it is not identity.
3. **The identity lock.** Every request needs `Tailscale-User-Login` equal to
   `HARBOR_ALLOWED_LOGIN`, compared in constant time.
4. **The app.** Strict CSP with build-time script hashes (no `unsafe-inline`), no
   CORS headers at all, non-root, read-only rootfs, `cap_drop: ALL`.

## Why the lock is a header and not "is it loopback"

Spike S10 (2026-11-18) measured this on the potato:

- A tailnet browser request through `tailscale serve` carried
  `Tailscale-User-Login`, `Tailscale-User-Name`, `X-Forwarded-For` and
  `X-Forwarded-Proto: https`.
- Inside the container the peer address was **172.17.0.1**, the Docker bridge
  gateway, for both a tailnet request *and* a `curl` from the host. "Is the peer
  loopback" cannot tell them apart, and a published port makes every caller look
  the same.
- A **forged** `Tailscale-User-Login` header on a tailnet request was overwritten
  by `tailscale serve` with the real login, so a tailnet client cannot choose its
  identity.

Consequences, in order of importance:

- `POST /api/update/*` and every future write route depend on the header being
  set by `tailscale serve` and *not* passed through. If the serve mapping ever
  changes to a mode that forwards client headers, the lock needs a second factor
  (`tailscale whois` against the peer, which the host-side unit can do) before
  the new mapping goes live.
- `GET /healthz` and `GET /api/status` are the only header-less exemptions, and
  only when the header is *absent*. A wrong login is refused on both. Neither
  exposes content: two fields and counts.
- `X-Forwarded-For` is diagnostic only. It is never identity.
- `HARBOR_DEV_NO_AUTH=1` exists for `make dev` and refuses to start when
  `HARBOR_DEPLOYMENT=docker`. There is no "auth off" mode on the potato.

## Egress: what Harbor may talk to

Harbor fetches arbitrary pages, PDFs and images, and calls the Worker and
OpenRouter. A hostname allowlist is therefore impossible — the destination is
whatever Autumn saved. That leaves two controls:

1. **A dial-time SSRF guard** (phase 3, `server/internal/fetch`): every outbound
   request resolves the host, refuses private, loopback, link-local, CGNAT and
   metadata addresses, refuses redirects to them, and enforces a timeout, a byte
   cap and a retry budget. This is the control that follows a redirect.
2. **The host firewall** (`deploy/docker-user-firewall.sh`): `DOCKER-USER` rules
   deny the container's fixed subnet (`172.31.240.0/24`) a *forwarded* route to
   `10/8`, `172.16/12`, `192.168/16`, `127/8`, `169.254/16`, `100.64/10`, and to
   `fd7a:115c:a1e0::/48`, `fc00::/7`, `fe80::/10`, `::1/128`. Everything else —
   the public internet — is allowed.

   **What this does not cover, deliberately and honestly:** `DOCKER-USER` is
   traversed for packets the host *forwards*. Traffic from the container to the
   host's own addresses (the bridge gateway, a LAN or tailnet address the host
   itself holds) is delivered locally through `INPUT`, so these rules do not see
   it. There is no useful way to blackhole the host from its own container
   without breaking DNS, so the host-local case is left to the dial-time SSRF
   guard and to the host's own input policy. Do not read this rule set as "the
   container cannot see the host".

   **The DNS trap:** if the potato resolves through a LAN resolver, Docker's
   embedded DNS forwards from the container subnet to that resolver, and these
   rules reject it — every fetch fails while `/healthz` stays green. `install.sh`
   runs a public-internet check after applying the rules and warns instead of
   pretending; if it fails, check `/etc/resolv.conf` on the host.

```bash
sudo deploy/docker-user-firewall.sh --check     # all rules present?
sudo deploy/docker-user-firewall.sh --apply     # idempotent
```

Docker's iptables rules do not survive a reboot, so
`deploy/harbor-firewall.service` re-applies them at boot (`install.sh` enables
it). Verify from inside a throwaway container on the same network:

```bash
docker run --rm --network harbor_egress curlimages/curl -sS --max-time 5 http://100.64.0.1/ ; # must fail
docker run --rm --network harbor_egress curlimages/curl -sS --max-time 5 https://example.com/ -o /dev/null -w '%{http_code}\n' ; # 200
```

## Writes, and what a rollback costs

The rootfs is read-only. The process may write only to:

- `/data` — `harbor.db` (+ `-wal`/`-shm`), `backups/pre-update.db`, later media
  and archives;
- `/update-signal` — the four signal files the settings page and watcher share;
- `/tmp` — a tmpfs, for extraction and image work.

Before every update the watcher runs `harbor snapshot --pre-update`, which is
`VACUUM INTO` → `PRAGMA integrity_check` → fsync → rename. A failed update
restores that file and re-pins the old digest.

**A rollback is not lossless.** Anything saved in-app during the health window is
lost; remote saves come back with `harbor replay --since <snapshot time>`
(phase 2). Migrations only go forward, which is why the snapshot is not optional.

## Token rotation drill

The Worker's door tokens can append and nothing else: no claim, no read, no
presign, no delete (design §4). Still, rotate them as if they could.

1. Generate a new token for one door in Settings → Sources (phase 4); Harbor
   stores a hash, shows the value once.
2. Update the one client that uses it — the Telegram webhook secret, the Shortcut.
3. Revoke the old token. One door at a time: a leaked token can add junk, not read.
4. If the Worker or R2 is ever suspected: rotate `HARBOR_WORKER_TOKEN` here, then
   the Worker secret, then check Status for jobs that arrived while it was wrong.

If the **GitHub App** credential in the pi-review workflow is compromised, it is
a build-time credential only: rotate the App private key in GitHub settings and
update the repository secret. It is not present on the potato and Harbor never
reads it.

## GHCR visibility

The package inherits visibility from the repository via the
`org.opencontainers.image.source` label. If the first `docker compose pull` fails
with a 401/403, the package is private: either make it public once, or
`docker login ghcr.io` on the potato with a read-only token. A private package
looks exactly like a broken deploy, which is why this is written down.
