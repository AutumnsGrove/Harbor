# LocalDrop — design

Distilled from [the research thread](research-thread.md), 2026-11-03. This is the *current best
shape*, not a spec. Anything marked **OPEN** is genuinely undecided — see
[open-questions.md](open-questions.md).

---

## 1. The concept

One inbox for links, notes, images, quotes — saved in one motion, never filed by hand. AI figures
out what each item is and organizes it. Search is associative, not folder-shaped.

The product is **the save gesture**, not the library UI. Raindrop is easy; mymind is easier. The
library only has to be good enough to trust and search.

**Non-goals for v1:** browser extension, native mobile app, sharing/collaboration, bulk import
UI, multi-user, public profiles. (See §11 for why each is deferred.)

---

## 2. Hard constraints

| Constraint | Consequence |
|---|---|
| The potato is RAM-starved (already runs her-go, SearXNG, AdGuard, Unbound, Polaris) | No headless Chromium, no local embedding model, no Postgres/Meilisearch stack on it. |
| The potato is tailnet-only | It cannot receive inbound calls from the public internet. |
| Saves must work from anywhere, instantly | A tailnet-only save endpoint is disqualified — Tailscale-on-iOS Shortcuts fail silently when the VPN isn't up. |
| One user, low volume (~20–100 saves/month) | No need for a search cluster, job framework, or auth system. |
| Must be cheap | Free tiers: Workers, D1, R2, Workers AI, Browser Rendering. |
| Must be ownable/portable | SQLite file on disk; export must be trivial. Data ownership was the whole point. |
| Same stack as the rest of the homelab | Go + embedded SvelteKit in one binary; Docker on the potato; GHCR + digest-pinned updater. |

---

## 3. Stack and deployment (settled 2026-11-03)

Follows the established shape from Warbler, Polaris, and bwmon — a Go binary with the SvelteKit
build embedded, shipped as a small Docker image, updated by a host-side watcher.

### The build

- **Backend:** Go, module `github.com/AutumnsGrove/LocalDrop`, server under `server/`, entry point
  `server/cmd/localdrop`. Static binary, `CGO_ENABLED=0`, distroless final image, non-root.
- **Frontend:** SvelteKit in `web/`, built to static `web/build`, embedded with
  `//go:embed all:build` (see Warbler's `web/embed.go`). No Node at runtime; one artifact to ship.
- **SQLite:** [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — pure Go, no cgo, so
  cross-compiles stay trivial. Same as Polaris and Ivy.
- **Version string:** `r<commit count>.<short sha>`, stamped with `-ldflags -X`, shown in Settings
  and in `/healthz`.
- **Makefile** with the usual targets: `dev`, `web`, `build`, `test`, `docker`, `docker-run`,
  `deploy`, `clean`.

### Why embed the frontend

One process to run, one binary to ship, no reverse proxy for static assets, no Node on a box with
almost no RAM. The SvelteKit app is served by the Go server, so auth, host checks, and the identity
lock apply to the UI and the API identically — there is no separate static origin to misconfigure.

### Container

```yaml
# docker-compose.yml at the repo root, like Warbler's, so Meridian's compose actions find it
services:
  localdrop:
    build: .
    image: ${LOCALDROP_IMAGE:-ghcr.io/autumnsgrove/localdrop:latest}
    restart: unless-stopped
    user: "${LOCALDROP_UID:-1000}:${LOCALDROP_GID:-1000}"
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    tmpfs: ["/tmp"]
    ports:
      - "127.0.0.1:${LOCALDROP_PORT:-8895}:8080"
    volumes:
      - ./data:/data
      - ./update-signal:/update-signal
```

- **Port 8895** (8893 is reserved for Cairn, 8894 for Coffer) — loopback only; `tailscale serve` is
  the only way in. Confirm it's free with `ss -ltn` before first install.
- **Data:** `~/LocalDrop/data/` — `localdrop.db` (+ WAL), `archives/` if anything is ever stored
  locally, `backups/` for the pre-update snapshot.
- **Deployment mode:** `LOCALDROP_DEPLOYMENT=docker`, surfaced in Settings.

### The update path (inherited, not reinvented)

This is the Warbler pattern, copied deliberately so there is one thing to understand across the
homelab:

```
Settings ──[Check]──► update-signal/check
                            │  localdrop-update.path sees the file
                            ▼
                    localdrop-update.service (oneshot)
                            │  resolve :latest -> digest
                            ▼
                    update-signal/available.json
Settings ──[Update]─► update-signal/requested (contents: "latest")
Meridian tap ───────► systemctl start localdrop-update.service  (no file)
                            │
                            ▼
                    1. pin the new digest in .env
                    2. VACUUM INTO a pre-update snapshot
                    3. pull + recreate
                    4. poll /healthz for up to 120s
                    5. healthy -> result "ok"; otherwise restore snapshot,
                       re-pin the old digest, recreate, result "failed"
```

- The app **never names an image**. It writes `requested` with the literal `latest`; the watcher
  owns the allowed repository and accepts only `<repo>@sha256:<64 hex>`.
- A `localdrop-tick.timer` every 10 minutes refreshes a heartbeat and catches missed signals. It
  never updates on its own.
- Endpoints: `GET /healthz` (public to Docker/Meridian, two fields), `GET /api/version`,
  `POST /api/update/check`, `GET /api/update/available`, `POST /api/update/start`,
  `GET /api/update/status`, `GET /api/busy`.
- Meridian target: `kind: compose`, `dir: /home/autumn/LocalDrop`, `service: localdrop`,
  `update: { kind: unit, unit: localdrop-update.service }`.
- The watcher never updates `docker-compose.yml`, the units, or the script themselves — those come
  from the checkout on the potato (`cd ~/LocalDrop && git pull && deploy/install.sh`).
- Rollback matters more here than for Warbler: SQLite migrations only go forward, so the snapshot
  restore is not optional.

### Access control

Same four layers as Warbler: tailnet → `Host` check → Tailscale identity lock
(`Tailscale-User-Login`) → app (no CORS, strict CSP, bundled assets, non-root, read-only rootfs).
**One deliberate difference:** LocalDrop must make outbound calls (OpenRouter, and the Cloudflare
Worker poll), so the image is not "no outside traffic" in the way Warbler is. Egress is allowlisted
in the security notes to exactly `openrouter.ai` and the Worker's hostname; nothing else.

### What CI does

On every push: `gofmt`, `go vet`, `go test -race`, pnpm check/test/build, the no-outside-addresses
frontend check. On a push to `main` that passes: build `linux/amd64` + `linux/arm64`, publish
`ghcr.io/autumnsgrove/localdrop` tagged `latest` and the short SHA. The potato only ever pulls; it
never builds.

## 4. Architecture

### The split: write path vs read path

```
 public internet                          tailnet
┌──────────────────────────┐          ┌──────────────────────────────┐
│ Cloudflare Worker        │          │ the potato                   │
│                          │          │                              │
│ POST /save  (write-only) │          │ web UI (read-write)          │
│ D1: jobs queue           │◄─────────│ poll loop (outbound only)    │
│ R2: archive blobs        │  GET     │ SQLite + FTS5 + vectors      │
│ Browser Rendering        │  /jobs   │ HTML parsing                 │
│ Workers AI embeddings    │          │ OpenRouter enrichment        │
└──────────────────────────┘          │ MCP tool → Polaris           │
        ▲                             └──────────────────────────────┘
        │ HTTPS                           ▲
   phone / browser / Telegram              │ Tailscale
   bookmarklet / Shortcut / email          you, anywhere
```

**Why this and not the obvious direction:** a Worker has no route into a tailnet — not slow, not
insecure, *no route*. So the potato is the HTTP **client**. It dials out to Cloudflare and pulls
work. That also means: no tunnel, no exposed port, no public IP, nothing to firewall, and the
potato can be asleep or rebooting while saves still land.

### Division of responsibility

| | Cloudflare Worker (public) | potato (tailnet) |
|---|---|---|
| Role | Always-on front door + renderer | Enrichment worker + database |
| Owns | `POST /save`, D1 queue, R2 blobs, Browser Rendering, embeddings | SQLite + FTS5 + in-process vectors, HTML parsing, OpenRouter calls, UI |
| Can read library | **never** | yes |
| Reachable from | public internet | outbound only |
| Fails when | almost never | fan dies, reboot, power cut |
| At risk | queue entries (durable in D1) | the library itself — back this up |
| Cost | $0 on free tier | $0 |

**Security property:** the public token can only append. A leaked token means someone can add
junk — not read, not delete, not enumerate. That is what makes the Worker safe to expose.

**The asymmetry to respect:** the potato holds the irreplaceable thing. The Worker holds a queue
nobody would mourn. Back up the potato; don't over-engineer the Worker.

---

## 5. Save path

One endpoint: `POST /save { link?, note?, image?, title? }` → writes a `queued` row in D1 →
returns `200` in ~50 ms. Nothing else happens on the request path.

Front doors, in the order they're worth building:

1. **floccus + a browser "Inbox" folder** — keep native `Cmd+D`, floccus syncs the folder to the
   API on a timer. Zero new UI, zero extension maintenance, works in every browser. Sneaky-best.
2. **Telegram bot** — a `POST` with a link/photo/forwarded message. Already on every device, and
   it handles "save this thought" as naturally as "save this URL."
3. **iOS/Android share sheet → Shortcut / PWA `share_target`** — one POST, appears in every app's
   share sheet. Solved pattern (Linkwarden and linkding both document it).
4. **Bookmarklet** — one line, unkillable, works where extensions can't.
5. **Email-to-save** — Cloudflare Email Routing → Worker. Good for newsletters and receipts.
6. **Tailnet quick-save page** — one input box, pinned to the phone home screen. Couch mode.
7. Machine inputs: Raycast/Alfred hotkey, NFC sticker, widget — all the same endpoint.

**Explicit non-goal:** a browser extension (Manifest V3 + store review + breaking on Chrome
updates is the single biggest reason not to build this yourself).

### The pending window

There is a gap between "save accepted" and "item in the library" (poll interval, or longer if the
potato was asleep). This must be visible, or the tool loses trust:

- The potato's poll response includes queued jobs; the UI renders them as greyed-out cards at the
  top. **Preferred.**
- Or: the UI triggers a poll on load. Simpler, slightly worse.

Do not skip this. "I saved it and it vanished" is the failure that kills adoption.

---

## 6. Enrichment pipeline

The potato's poll loop is single-threaded with a `sleep`, one job at a time. Concurrency is what
turns a 60 MB spike into a 300 MB OOM on this box.

Per job:

1. **Fetch** the URL with a `Content-Length` cap (~5 MB) and a timeout. Stream to a temp file.
2. **Parse from disk** (`selectolax` / `trafilatura`) so peak RSS stays flat. A 4 MB HTML doc
   parsed into a DOM is 40–80 MB resident — the only real memory spike in the system.
3. **Ask the Worker to render** (screenshot + PDF + single-file HTML) → bytes go to R2; the
   potato stores only the R2 key/URL. This keeps the SD card from filling with 8 MB PDFs.
4. **Classify + summarize** via a cheap OpenRouter model: what it *is* (article / product / book /
   recipe / video / tweet), tags, a summary. Structured JSON output.
5. **Embed** — Workers AI (10k neurons/day free) or OpenRouter; store the vector.
6. **Write** the row + FTS5 + vector, mark the D1 job done.

Failures: retry with backoff, then park the job as `failed` with the error kept. A bad URL must
never wedge the queue.

**Cost reality check:** a ~2,000-token page in and ~300-token structured output out on a
Gemini-Flash-class model is ~**$0.00048/save** — under a dollar a year at realistic volume. The
expensive subscription tier wasn't paying for inference; it was paying for the UI and the gesture.

---

## 7. Data model (sketch — **OPEN** in details)

```sql
CREATE TABLE items (
  id            TEXT PRIMARY KEY,
  url           TEXT,                  -- canonicalized; NULL for pure notes
  title         TEXT NOT NULL DEFAULT '',
  note          TEXT NOT NULL DEFAULT '',   -- the user's own words
  kind          TEXT NOT NULL DEFAULT 'unknown', -- article/product/book/recipe/video/note/image
  summary       TEXT NOT NULL DEFAULT '',
  content_text  TEXT NOT NULL DEFAULT '',   -- extracted text, feeds FTS5
  og_json       TEXT NOT NULL DEFAULT '{}', -- cover, description, site, author, published
  state         TEXT NOT NULL DEFAULT 'queued', -- queued|enriching|ready|failed
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE item_tags (item_id TEXT, tag_id INTEGER, PRIMARY KEY (item_id, tag_id));

CREATE TABLE archives (
  item_id   TEXT PRIMARY KEY,
  r2_key    TEXT,                      -- html / pdf / screenshot objects
  bytes     INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE VIRTUAL TABLE items_fts USING fts5(
  title, summary, content_text, note, content='items', content_rowid='rowid'
);

CREATE TABLE item_embeddings (
  item_id      TEXT PRIMARY KEY,
  model        TEXT NOT NULL,
  content_hash TEXT NOT NULL,        -- skip re-embedding unchanged rows
  vec          BLOB NOT NULL,        -- float32 little-endian; no extension needed
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

Open modelling questions: notes vs items as separate tables, attachments (multi-file), tag
synonyms/merging, source of save (floccus/telegram/shortcut), and whether highlights/annotations
belong in v1.

Embedding dimension is deliberately not hard-coded: `model` plus `content_hash` per row makes a
model swap detectable and re-embeddable, and the brute-force cache reads whatever is stored.

---

## 8. Search

- **FTS5** for keyword search — built into SQLite, no extra process, good enough for one user.
- **Exact brute-force cosine** over embeddings held in a process-local slice, loaded at startup and
  appended by the indexer — *not* `sqlite-vec`. Polaris already does exactly this (its
  `docs/plans/embeddings.md` sets the revisit point at ~50k vectors); at one user's library scale
  an ANN index is pure complexity, and `sqlite-vec` would force cgo into an otherwise pure-Go,
  static binary. Vectors live in `item_embeddings.vec` as a `BLOB`; SQLite stays the source of
  truth, the in-memory slice is a cache with a rebuild path.
- Combine keyword + semantic into one ranked list (reciprocal rank fusion or a weighted blend).
  **OPEN:** ranking weights.
- Later, maybe: colour/brand/visual search (mymind's "Same Vibe" is cosine similarity over
  embeddings you're already generating).

---

## 9. UI (**OPEN** — deliberately minimal)

- One screen: search box, grid of cards, click to open; reader view for articles.
- Save-status surface: pending cards at the top.
- No folders, no manual sorting, no views toggle, no settings maze.
- Frontend question (Svelte vs server-rendered Go/htmx vs plain) is unresolved — see open
  questions.

---

## 10. Polaris integration (the payoff)

An MCP tool — `search_bookmarks(query, limit?)` — pointed at the potato's SQLite. This is the
reason the project is worth building instead of self-hosting Karakeep: Polaris already has the
agent harness, the MCP pattern, and the tailnet.

Possible surface: `search_bookmarks`, `save_bookmark`, `get_bookmark`, `list_recent`. **OPEN:**
read-only vs read-write, and whether save-by-voice ("Polaris, save this") is in scope.

---

## 11. Operations

- **Backup:** nightly `VACUUM INTO` (never `.backup` — the online-backup API livelocked against a
  live writer on this box once already; see `homelab/docs/coffer.md`), compressed and tiered from
  day one, → R2. This is the only failure in the system that is actually bad. Test the restore.
- **Pre-update snapshot:** `VACUUM INTO data/backups/pre-update.db` through the running container,
  restored automatically if the new image fails its health check.
- **Export:** JSON + Markdown + Netscape HTML, one command. Data ownership is the point — never
  build a roach motel like mymind's one-way export.
- **Secrets:** `SAVE_TOKEN` in a Worker secret; `OPENROUTER_API_KEY` held by the potato. Rotate
  the token without redeploying the potato where possible.
- **Rate limiting:** Workers rate-limit binding on `/save` (a few/min/IP). Treat as
  abuse-dampening, not a hard guarantee.
- **Cost guardrails:** cap LLM tokens per page, cap page bytes, cap archive size; alert if D1/R2
  usage moves.
- **Deployment:** Docker Compose alongside the other potato services; health endpoint for the poll
  loop's last-success timestamp.

---

## 12. Build order

Each step is independently useful. Steps 1–2 alone are a working bookmark manager; everything
after is enrichment that can't break the save path.

| # | Step | Why now | Est. |
|---|---|---|---|
| 0 | Phase 0 plumbing: repo skeleton, embedded SvelteKit, image, compose, GHCR publish, update watcher, `/healthz`, identity lock | Warbler's lesson: build the update path before the features, not after | a weekend |
| 1 | Worker: `POST /save` → D1 | Saves work immediately, even with nothing behind them | 1 evening |
| 2 | Potato: poll loop + SQLite schema | Saves actually land | 1 evening |
| 3 | Worker: Browser Rendering endpoint | Archives without loading the potato | a weekend |
| 4 | Potato: OpenRouter enrichment (kind/tags/summary) | The library starts organizing itself | a weekend |
| 5 | FTS5, then brute-force vector search | Search you'd actually use | 1 evening each |
| 6 | MCP tool → Polaris | The payoff | 1 evening |
| 7 | Front doors: floccus, Telegram, Shortcut | Only matters once it's trustworthy | 1 evening each |
| 8 | Minimal UI polish, reader view, pending cards | Daily-use quality | ongoing |

Deliberately absent: extension, native apps, sharing, collaboration, import UI.

---

## 13. Decisions log

**Settled**

- Personal, single-user, tailnet-hosted; no accounts, no billing, no multi-tenancy.
- Public write-only Worker; private read-write potato; potato polls outbound.
- SQLite on the potato via `modernc.org/sqlite` + FTS5. No Postgres, no Meilisearch.
- Embeddings are exact brute-force cosine in Go over an in-memory cache — no `sqlite-vec` (cgo
  would break the static, cross-compiled binary; Polaris set the same precedent).
- Go backend + SvelteKit embedded with `go:embed`, shipped as a distroless Docker image.
- Docker Compose on the potato, GHCR multi-arch images, digest-pinned `.env`.
- Updates use the Warbler watcher: systemd path unit + oneshot service + tick timer, automatic
  health check, rollback, and pre-update DB snapshot. Meridian can trigger it.
- Save path never depends on enrichment succeeding.
- Non-goals: no browser extension, no native mobile app in v1 (PWA if anything).
- Archive blobs in R2; the potato stores keys, not files.
- Cheap remote models for AI; nothing runs locally.
- Polaris MCP integration is the reason to build it.

**Leaning (not locked)**

- floccus + Telegram as the two primary front doors.
- ~~Go for the potato service~~ — **settled: Go** (Python's parser ecosystem loses to deployment
  and memory simplicity).
- ~~Svelte for the UI~~ — **settled: SvelteKit** (embedded static build).
- Port 8895 (proposed; 8893 Cairn, 8894 Coffer already reserved).
- Notes and links share one `items` table.
- Reader view + highlights are v1.5, not v1.
- Public collection sharing: skip entirely.

---

## 14. References

- [Raindrop Pro features](https://help.raindrop.io/premium-features) ·
  [pricing](https://contextbolt.com/blog/raindrop-pricing/)
- [mymind](https://mymind.com/) · [2026 review](https://www.marqly.com/blog/mymind-review-2026)
- [Karakeep](https://github.com/karakeep-app/karakeep) ·
  [Linkwarden](https://github.com/linkwarden/linkwarden) ·
  [linkding](https://github.com/sissbruecker/linkding) ·
  [Shiori](https://github.com/go-shiori/shiori)
  [monolith](https://github.com/Y2Z/monolith) · [floccus](https://floccus.org/)
- [Cloudflare Browser Rendering pricing](https://developers.cloudflare.com/browser-run/pricing/) ·
  [D1 pricing](https://developers.cloudflare.com/d1/platform/pricing/) ·
  [R2 pricing](https://developers.cloudflare.com/r2/pricing/) ·
  [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) ·
  [Workers AI pricing](https://developers.cloudflare.com/workers-ai/platform/pricing/)
- Full cited list: [research-thread.md](research-thread.md)
