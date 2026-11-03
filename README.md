# LocalDrop

> **Status: concept.** No code yet. Name is a placeholder. The goal of this repo right now is to
> nail the shape of the thing before any of it exists.

A personal save-anything library for one person, built to replace a Raindrop Pro + mymind
subscription ($157/year) with something owned outright.

Not a Raindrop clone. Raindrop is a *library*; mymind is a *gesture*. This is built around the
gesture — saving has to be as easy as `Cmd+D` or sharing a link to a chat — with an AI-enriched
library that assembles itself behind it.

**Design principle:** the save path must never fail. Everything else is enrichment that can be
added, rebuilt, or skipped without breaking it.

**The payoff:** Polaris gets a `search_bookmarks()` MCP tool, so the assistant can search the
library. That integration is the one thing no product can sell.

**Stack (settled):** Go + embedded SvelteKit in a single static binary, SQLite (`modernc`), shipped
as a distroless Docker image on `ghcr.io`, updated by the same digest-pinned systemd watcher as
Warbler. Python was ruled out on deployment/memory cost — the parser has to work in Go.

---

## The shape (60 seconds)

```
 phone / browser / Telegram / bookmarklet
                 │  HTTPS, always works
                 ▼
     Cloudflare Worker  ── public, write-only
     POST /save + /upload → D1 queue + R2 intake
     Browser Rendering, Workers AI embeddings
                 ▲
                 │  potato polls outbound (no tunnel, no open ports)
                 ▼
     the potato (tailnet) ── private, read-write
     SQLite + FTS5 + in-process vectors · enrichment via OpenRouter
     minimal web UI · MCP tool → Polaris
```

The Worker can't reach the tailnet, so the potato polls the Worker — the potato is the client,
never the server. The Worker can only *append*; it can never read the library. That's what makes
it safe on the public internet.

- **The potato holds the irreplaceable thing** (the SQLite library + local media) → back it up
  nightly to R2.
- **The Worker holds a durable queue and the intake copy of every remote blob** → saves survive
  potato downtime, reboots, and a dying fan. Remote images are in R2 before the potato ever sees
  them; in-app images mirror up afterwards.
- **Heavy work goes to the edge** (headless Chromium, embeddings, LLM calls) because the potato
  is already running her-go, SearXNG, AdGuard, Unbound, and Polaris.

---

## What exists so far

| File | What it is |
|---|---|
| [docs/design.md](docs/design.md) | The concept distilled: stack, deployment/update path, components, data flow, build order, decisions |
| [docs/open-questions.md](docs/open-questions.md) | What still needs a decision before code — the agenda for the next Q&A pass |
| [docs/research-thread.md](docs/research-thread.md) | Verbatim Polaris thread that produced the direction (104 cited sources) |

## Deployment shape

- `~/LocalDrop` on the potato, Compose project `localdrop`, data in `~/LocalDrop/data`.
- Port **8895** (proposed; 8893 Cairn and 8894 Coffer are already reserved), loopback only behind
  `tailscale serve`.
- Update path is Warbler's: Settings → signal file → host watcher → digest pin → health check →
  rollback with a pre-update `VACUUM INTO` snapshot. Meridian can trigger it too.
- CI publishes multi-arch images on every passing push to `main`; the potato never builds.

## What this is not (yet)

No browser extension, no mobile app, no collaboration, no sharing, no bulk-import UI. Those turn
a weekend project into a second job. If the project earns it later, it can grow them.

## Prior art worth knowing

Karakeep (closest feature match, heavy: Postgres + Meilisearch + Chromium), Linkwarden
(preservation-first, also heavy), linkding (light Django/SQLite), Shiori (light, single Go
binary). None of them plug into Polaris. That's the gap.
