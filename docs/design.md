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

### v1 scope (settled 2026-11-03)

- **Save:** links, notes, **and images**. Images take two paths: in-app saves write to the
  potato's disk first; remote saves (Telegram, Shortcut) are uploaded to R2 by the Worker at
  ingestion, then pulled down to the potato. Either way the potato keeps a local copy and R2 keeps
  a durable copy.
- **Organize:** AI `kind` + AI tags + **optional manual tags** + **Smart Spaces**.
- **Read:** reader view, **highlights, and annotations on highlights**.
- **Front doors:** in-app save composer, Telegram bot, iOS Shortcut / PWA share target, tailnet
  quick-save page.
- **Not:** browser extension, native app, public sharing, collaboration, multi-user.

This is bigger than the original "weekend MVP". The build order phases it so save + search is
usable before images, highlights, and spaces land; nothing in this list is cut.

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
│ POST /save + /upload     │          │ web UI (read-write)          │
│ D1: jobs queue           │◄─────────│ poll loop (outbound only)    │
│ Browser Rendering        │  GET     │ SQLite + FTS5 + vectors      │
│ Workers AI embeddings    │  /jobs   │ image files (local copy)     │
│ R2: intake + mirror      │          │ HTML parsing                 │
│                          │          │ OpenRouter enrichment        │
│                          │          │ MCP tool → Polaris           │
└──────────────────────────┘          └──────────────────────────────┘
        ▲                             ▲
        │ HTTPS                       │ Tailscale
   in-app / Telegram / Shortcut / quick-save page
```

**Blob split (revised 2026-11-03):** the Worker is the ingestion point for every remote save, so
it uploads remote blobs to R2 *immediately* — streaming the request body straight to R2, then
writing the D1 job row with the key and SHA-256. The potato pulls the blob down on its next poll,
verifies the hash, and keeps the local copy that the UI serves from. In-app saves invert it: write
locally, mirror up to R2 afterwards. R2 is therefore both the durable intake buffer and the
off-box copy; the potato's local copy is what makes the app fast and independent of R2.

Worker-rendered archives (screenshot/PDF/HTML) follow the same intake rule: the Worker writes them
straight to R2 because it has no route to the potato.

**Why this and not the obvious direction:** a Worker has no route into a tailnet — not slow, not
insecure, *no route*. So the potato is the HTTP **client**. It dials out to Cloudflare and pulls
work. That also means: no tunnel, no exposed port, no public IP, nothing to firewall, and the
potato can be asleep or rebooting while saves still land.

### Division of responsibility

| | Cloudflare Worker (public) | potato (tailnet) |
|---|---|---|
| Role | Always-on front door + renderer | Enrichment worker + database |
| Owns | `POST /save` + `/upload`, D1 queue, R2 intake/mirror, Browser Rendering, embeddings | SQLite + FTS5 + in-process vectors, local image files, HTML parsing, OpenRouter calls, UI |
| Can read library | **never** | yes |
| Reachable from | public internet | outbound only |
| Fails when | almost never | fan dies, reboot, power cut |
| At risk | queue entries + unclaimed R2 blobs | the library itself — database **and** local images |
| Cost | $0 on free tier | $0 |

**Security property:** the public token can only append. A leaked token means someone can add
junk — not read, not delete, not enumerate. That is what makes the Worker safe to expose.

**The asymmetry to respect:** the library's meaning lives on the potato — the database is the one
thing that cannot be rebuilt. R2 holds durable copies of blobs (and the intake copy of every remote
save), but a bucket full of orphaned images is not a library. Back up the database and the media
mirror; don't over-engineer the Worker.

---

## 5. Save path

Two ingestion routes on the Worker:

- `POST /save { link?, note?, title? }` → writes a `queued` row in D1 and returns `200` in ~50 ms.
- `POST /upload` (multipart or raw body, one file per request) → **streams the body straight into
  R2** (`env.BUCKET.put(key, request.body)` — never buffered in the Worker), then writes the D1
  job row with `r2_key`, `content_type`, and `bytes`. Returns `200` as soon as R2 has the object.
  The SHA-256 is computed on the potato when it pulls the blob down, because hashing a 25 MB stream
  in a Worker would mean buffering it.

Both are token-gated and rate-limited, with a content-type allowlist and a hard size cap. A remote
save is "accepted" the moment its blobs are in R2 and the job row exists — the potato catching up
is enrichment, not acceptance.

Front doors (settled):

1. **In-app save composer** — paste a URL, write a note, upload images. The UI already runs on the
   potato, so this writes to the local database and enters the same enrichment queue directly; it
   does not round-trip through the Worker.
2. **Telegram bot** — links, notes, photos, forwarded messages. The best mobile gesture, and the
   only door that handles "save this thought" as naturally as "save this URL."
3. **iOS Shortcut / PWA `share_target`** — the share sheet in every app → `POST /save` on the
   Worker. Works even when the potato is asleep or the VPN is off.
4. **Tailnet quick-save page** — one input box pinned to the phone's home screen. Couch mode; only
   works on the tailnet, which is the tradeoff for having no public token in it.

Considered and dropped: floccus (it targets WebDAV/Nextcloud, not arbitrary JSON), the browser
bookmarklet, email-to-save, and a browser extension (Manifest V3 + store review + breaking on
Chrome updates is the single biggest reason not to build this yourself).

### The pending window

Remote doors have a gap between "save accepted" and "item in the library" (poll interval, or
longer if the potato was asleep). The blob is already safe in R2 during that gap, so the pending
card can even show it from a short-lived signed URL. In-app saves skip the gap by writing locally. For the rest, this must
be visible, or the tool loses trust:

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
   potato stores the R2 key. Rendered archives are the one blob type that skips local storage.
4. **Classify + summarize** via a cheap OpenRouter model: what it *is* (article / product / book /
   recipe / video / tweet), tags, a summary. Structured JSON output.
5. **Embed** — Workers AI (10k neurons/day free) or OpenRouter; store the vector.
6. **Write** the row + FTS5 + vector, mark the D1 job done.

**Image jobs** (Telegram photo, upload, pasted image) take a different path, and which direction
they travel depends on where they started:

1. **Get the bytes.** Remote images are already in R2 — the poll response carries `r2_key`; the
   potato streams them down and hashes them in the same pass. In-app images are already on local
   disk.
2. **Store locally**, content-addressed at `data/media/<sha256>` (dedupe for free), and generate a
   thumbnail. The hash computed during the download becomes the `media.sha256` and the local
   filename.
3. **Look** via a vision model: kind, tags, OCR text, a one-line description, dominant colours.
4. **Embed** OCR + description so images are searchable by their contents.
5. **Mirror up.** In-app images upload to R2 in the background and fill in `media.r2_key`; remote
   images are already there.
6. **Write** the row(s), mark the job done.

The same content-addressed layout means an image that arrives twice (once from Telegram, once
pasted into the app) is stored once, whichever door got there first.

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
  source        TEXT NOT NULL DEFAULT 'app', -- app|telegram|shortcut|quick
  state         TEXT NOT NULL DEFAULT 'queued', -- queued|enriching|ready|failed
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE item_tags (
  item_id TEXT NOT NULL,
  tag_id  INTEGER NOT NULL,
  source  TEXT NOT NULL DEFAULT 'ai',  -- ai|manual
  PRIMARY KEY (item_id, tag_id)
);

-- Originals live on the potato; R2 is a mirror.
CREATE TABLE media (
  id           TEXT PRIMARY KEY,
  item_id      TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  kind         TEXT NOT NULL,              -- image|pdf|file|thumbnail
  filename     TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT '',
  bytes        INTEGER NOT NULL DEFAULT 0,
  sha256       TEXT NOT NULL,              -- content address + mirror integrity
  width        INTEGER,
  height       INTEGER,
  local_path   TEXT NOT NULL,              -- relative to data/media
  r2_key       TEXT NOT NULL DEFAULT '',   -- set once mirrored
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX media_by_item ON media(item_id);

-- Reader highlights; anchor is a text-position selector, not a CSS path.
CREATE TABLE highlights (
  id          TEXT PRIMARY KEY,
  item_id     TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  quote       TEXT NOT NULL,
  note        TEXT NOT NULL DEFAULT '',    -- annotation on the highlight
  color       TEXT NOT NULL DEFAULT '',
  anchor_json TEXT NOT NULL,               -- quote + prefix/suffix + ordinal
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Smart Spaces: 'auto' is rule-driven, 'saved' is an explicit query.
CREATE TABLE spaces (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL DEFAULT 'auto',
  rule_json  TEXT NOT NULL DEFAULT '{}',   -- tags, kind, date range, text, similarity
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE space_items (
  space_id TEXT NOT NULL,
  item_id  TEXT NOT NULL,
  PRIMARY KEY (space_id, item_id)
);

-- Worker-rendered snapshots (never stored locally; referenced by key).
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

Open modelling questions: multiple attachments per item (the table allows it; the UI picks), tag
synonyms/merging, the highlight anchor format and how it survives re-extraction, and how much of a
Smart Space is a stored rule vs a live query.

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

## 9. UI (SvelteKit, embedded)

Five surfaces, no folders and no settings maze:

- **Home** — search box, Spaces row, pending cards, then the recent grid. Cards show cover/media,
  kind, title, tags.
- **Save composer** — URL, note, image upload. Reachable from Home and from the quick-save page on
  the phone. Same endpoint shape as the remote doors, written locally.
- **Item** — media, AI + manual tags, summary, source, archive link, related items.
- **Reader** — extracted article view with select-to-highlight and an annotation field. Highlights
  are also listed on the item.
- **Settings** — version, update check/apply (Warbler pattern), export, storage/mirror status.

Smart Spaces appear as a horizontal row of chips below search; `auto` spaces are computed from
their rule, `saved` ones open a query. Colour/brand/visual search is a later addition, not a v1
surface.

---

## 10. Polaris integration (the payoff)

An MCP tool — `search_bookmarks(query, limit?)` — pointed at the potato's SQLite. This is the
reason the project is worth building instead of self-hosting Karakeep: Polaris already has the
agent harness, the MCP pattern, and the tailnet.

Possible surface: `search_bookmarks`, `save_bookmark`, `get_bookmark`, `list_recent`. **OPEN:**
read-only vs read-write, and whether save-by-voice ("Polaris, save this") is in scope.

---

## 11. Operations

- **Blob layout:** local originals are content-addressed at `data/media/<sha256[0:2]>/<sha256>`,
  with generated thumbnails alongside. Content addressing gives dedupe and a hash to verify every
  copy. Thumbnails are derived and rebuildable; originals are not.
- **R2 intake keys:** the Worker streams uploads to `incoming/<job-id>/<n>`, because it cannot hash
  a streamed body before writing it. The potato hashes on pull and records the real SHA-256; the
  R2 object is left where it landed and referenced by `media.r2_key`. (A later normalisation pass
  could copy it to a content-addressed key, but nothing depends on that.)
- **Mirror directions:** remote saves go **in** via the Worker; in-app saves go **up** via an
  idempotent background task that uploads any `media` row with an empty `r2_key`. Mirror failures
  never block saves; they show as a "not mirrored" count in Settings.
- **Orphan cleanup:** if an R2 upload succeeds but the D1 insert fails, the object is unreferenced.
  A daily Worker cron deletes `incoming/` objects older than 7 days with no matching job row. The
  reverse case — a job row whose object is gone — is a retryable `failed` job, not data loss.
- **Backup:** nightly `VACUUM INTO` (never `.backup` — the online-backup API livelocked against a
  live writer on this box once already; see `homelab/docs/coffer.md`), compressed and tiered from
  day one, → R2. The database **and** the media mirror are both required for a real restore. Test
  the restore.
- **Pre-update snapshot:** `VACUUM INTO data/backups/pre-update.db` through the running container,
  restored automatically if the new image fails its health check.
- **Disk guardrails:** per-file upload cap (~25 MB), a storage number in Settings, and a warning
  before the SD card gets close. The potato has room, but not infinite room.
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

Each phase exits with something usable. Steps 0–4 are already a working link + note manager; the
rest adds the v1 scope without ever breaking the save path.

| # | Step | Phase exit | Est. |
|---|---|---|---|
| 0 | Plumbing: repo skeleton, embedded SvelteKit, image, compose, GHCR publish, update watcher, `/healthz`, identity lock | Deploys and updates itself | a weekend |
| 1 | Worker: `POST /save` + `POST /upload` → D1 + R2 | Saves work even with nothing behind them, and blobs are durable immediately | 1 evening |
| 2 | Potato: poll loop, schema, in-app save composer | Saves land; links + notes usable | 1 evening |
| 3 | Fetch + extract + classify + FTS5 | Library organizes itself and is searchable | a weekend |
| 4 | Telegram bot, Shortcut/PWA share, quick-save page | The gesture works from every device | 1 evening each |
| 5 | Images: Worker intake → R2, potato pull + hash, content-addressed local store, vision/OCR, thumbnails, mirror-up | Photos and screenshots save and search | a weekend |
| 6 | Reader view + highlights + annotations | Reading happens in the app | a weekend |
| 7 | Manual tags + Smart Spaces (rules, chips, saved queries) | Browsing by theme, not just search | a weekend |
| 8 | Brute-force vector search (hybrid ranking) | "That article about the thing" | 1 evening |
| 9 | Archive rendering via Worker → R2 | Links keep their contents | a weekend |
| 10 | MCP tool → Polaris | The payoff | 1 evening |
| 11 | Export command, backup restore drill, storage/mirror UI | Trustworthy to rely on daily | ongoing |

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
- v1 saves links, notes, **and images**.
- Organizing is AI `kind` + AI tags, with manual tags allowed and Smart Spaces as a first-class
  navigation surface.
- Reader view, highlights, and annotations on highlights are in v1.
- Front doors: in-app composer, Telegram, iOS Shortcut / PWA share target, tailnet quick-save
  page. No floccus, bookmarklet, email-to-save, or extension.
- Public collection sharing: **skipped entirely** — no public read path exists.
- Non-goals: no browser extension, no native mobile app (PWA if anything).
- The Worker is the ingestion point for remote saves: it streams blobs into R2 immediately, then
  the potato pulls, verifies, and stores them locally (content-addressed). In-app saves write
  locally and mirror up. R2 is both the durable intake buffer and the off-box copy.
- Worker-rendered archives are written straight to R2 and referenced by key; they are never pulled
  down to the potato.
- Cheap remote models for AI; nothing runs locally.
- Polaris MCP integration is the reason to build it.

**Leaning (not locked)**

- ~~Go for the potato service~~ — **settled: Go** (Python's parser ecosystem loses to deployment
  and memory simplicity).
- ~~Svelte for the UI~~ — **settled: SvelteKit** (embedded static build).
- Port 8895 (proposed; 8893 Cairn, 8894 Coffer already reserved).
- Notes and links share one `items` table; images are `items` with `media` rows.
- Smart Spaces are stored rules (`auto`) evaluated live, plus explicit saved queries (`saved`)
  rather than a materialised membership table — `space_items` exists only for manual pinning.
- Highlight anchoring uses a text-quote selector (quote + prefix/suffix + ordinal), not DOM paths,
  so it survives re-extraction.

**Still open** (see [open-questions.md](open-questions.md))

- Embedding model + dimensions; hybrid ranking weights.
- Highlight anchor edge cases and re-anchoring UX.
- Smart Space rule vocabulary.
- R2 access (presigned vs proxy) for archives.
- Backup/mirror schedule and retention.

---

## 14. References

- [Raindrop Pro features](https://help.raindrop.io/premium-features) ·
  [pricing](https://contextbolt.com/blog/raindrop-pricing/)
- [mymind](https://mymind.com/) · [2026 review](https://www.marqly.com/blog/mymind-review-2026)
- [Karakeep](https://github.com/karakeep-app/karakeep) ·
  [Linkwarden](https://github.com/linkwarden/linkwarden) ·
  [linkding](https://github.com/sissbruecker/linkding) ·
  [Shiori](https://github.com/go-shiori/shiori)
- Go parsing: [go-readability](https://github.com/go-shiori/go-readability) ·
  [goquery](https://github.com/PuerkitoBio/goquery) · [monolith](https://github.com/Y2Z/monolith)
- Homelab precedents: `Warbler/docs/update.md`, `Polaris/docs/plans/embeddings.md`,
  `homelab/docs/coffer.md` (VACUUM INTO rule)
- [Cloudflare Browser Rendering pricing](https://developers.cloudflare.com/browser-run/pricing/) ·
  [D1 pricing](https://developers.cloudflare.com/d1/platform/pricing/) ·
  [R2 pricing](https://developers.cloudflare.com/r2/pricing/) ·
  [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) ·
  [Workers AI pricing](https://developers.cloudflare.com/workers-ai/platform/pricing/)
- Full cited list: [research-thread.md](research-thread.md)
