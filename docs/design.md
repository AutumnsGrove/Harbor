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
- **Organize:** AI `kind` (12 kinds) + AI tags + **optional manual tags** + **Smart Spaces**,
  with a global tag manager and multi-select bulk actions.
- **Read:** reader view, **highlights, and annotations on highlights**.
- **Front doors:** in-app save composer, Telegram bot, iOS Shortcut / PWA share target, tailnet
  quick-save page.
- **Import:** Raindrop CSV and Netscape HTML, so the switch from the paid service is real.
- **Read later:** an optional hand-curated collection, not a read/unread state. The library stays
  a library; a queue product is a later version if it's ever wanted.
- **Editing:** Markdown notes, and every field (title, note, tags, kind, Read later) editable.
- **Delete:** Recently deleted for 30 days, then purge.
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
| Must be cheap | Free tiers: Workers, D1, R2, Browser Rendering; OpenRouter cents. |
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
- **AI:** all AI goes through **Nova** (`github.com/AutumnsGrove/nova`), the in-house Go agent
  harness, with one OpenRouter key. Three surfaces:
  - `decide` + `typesafe/jev-1.14` for **classification** (typed choice questions, probabilities,
    no generated text).
  - `provider/openrouter` + `deepseek/deepseek-v4.1-flash-1017` for **tags, summary, title**, and
    the same model (it accepts image input) for **OCR / image description / image tags**.
  - `embed` + `perplexity/pplx-embed-v1-0.6b` (1024 dims) for **vectors**. Nova's embed primitive
    is issue [#49](https://github.com/AutumnsGrove/nova/issues/49); LocalDrop is its first
    consumer, and semantic search waits on it.
  Enrichment is a pipeline of one-shot calls, not an agent loop. `message.Parts` carries images to
  the vision-capable chat model.
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
│ embeddings (via Nova)    │  /jobs   │ image files (local copy)     │
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
2. **Telegram bot** — a **dedicated bot** (own token, Worker webhook, isolated from Mira). Accepts
   links, text notes, photos, documents, and albums; albums group by `media_group_id`. The best
   mobile gesture, and the only door that handles "save this thought" as naturally as "save this
   URL."
3. **iOS Shortcut / PWA `share_target`** — the share sheet in every app → `POST /save` on the
   Worker. Works even when the potato is asleep or the VPN is off.
4. **Tailnet quick-save page** — one input box pinned to the phone's home screen. Couch mode; only
   works on the tailnet, which is the tradeoff for having no public token in it.

Considered and dropped: floccus (it targets WebDAV/Nextcloud, not arbitrary JSON), the browser
bookmarklet, email-to-save, and a browser extension (Manifest V3 + store review + breaking on
Chrome updates is the single biggest reason not to build this yourself).

### Re-saving is a merge, not a duplicate

One canonical item per canonical URL. Saving an existing URL:

- appends the new note (separated, timestamped), unions tags, keeps Read later state,
- refreshes metadata, archive, and screenshot,
- bumps `updated_at` so the item resurfaces.

Canonicalisation lowercases the host, strips the fragment and common tracking parameters
(`utm_*`, `fbclid`), and keeps the rest of the query. A manual **Refresh** action on an item runs
the same path without adding a note. `items.url` carries a partial unique index.

### Import

`Settings → Import` (and a CLI for big files) accepts **Raindrop CSV** and **Netscape HTML**:

- CSV: title, note, excerpt, URL, tags, created date, cover, and highlights are preserved;
  Raindrop collections become **manual Spaces**.
- HTML: URL, title, and folder; folders become manual Spaces.
- Import is idempotent by canonical URL and merges like a re-save, so running it twice is safe.
- Imported items enter the normal enrichment queue — no special pipeline.

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
2. **Parse from disk** with `go-readability` + `goquery` so peak RSS stays flat. A 4 MB HTML doc
   parsed into a DOM is 40–80 MB resident — the only real memory spike in the system.
3. **Ask the Worker to render.** Every save gets a single-file **HTML** archive and a
   **screenshot** (used as the card); **PDF is on demand**. Bytes go to R2 and the potato stores
   the R2 key. Rendered archives are the one blob type that skips local storage.
4. **Classify** through Nova's `decide` with Jev (`typesafe/jev-1.14`): a `choice` question over
   the 12 kinds — **article, product, book, recipe, video, tweet, note, image, paper, podcast,
   place, movie** — plus optional `noul` checks (`is_paywalled`, `is_video`). `article` is the
   catch-all for written pages and unclassifiable links (including code repos); the model is never
   allowed to answer `unknown`. Typed answers with probabilities, no text to parse.
5. **Tag + summarize** with `deepseek/deepseek-v4.1-flash-1017`: 3–6 tags, a two-sentence summary,
   a cleaned title. Free-form text, strict JSON schema, prompt version stored.
6. **Embed** with Nova's `embed` surface and `perplexity/pplx-embed-v1-0.6b` (1024 dims). The
   vector is unnormalized, so compare with cosine; store it as a BLOB.
7. **Write** the row + FTS5 + vector, mark the D1 job done.

**Image jobs** (Telegram photo, upload, pasted image) take a different path, and which direction
they travel depends on where they started:

1. **Get the bytes.** Remote images are already in R2 — the poll response carries `r2_key`; the
   potato streams them down and hashes them in the same pass. In-app images are already on local
   disk.
2. **Store locally**, content-addressed at `data/media/<sha256>` (dedupe for free), and generate a
   thumbnail. The hash computed during the download becomes the `media.sha256` and the local
   filename.
3. **Look** with `deepseek/deepseek-v4.1-flash-1017` (image input): kind, tags, OCR text, a
   one-line description, dominant colours. Thumbnails (max 1600px) are generated on the potato
   with Go's `image` package, so the Worker stays tiny.
4. **Embed** OCR + description with the same `pplx-embed-v1-0.6b` model so images are searchable
   by their contents.
5. **Mirror up.** In-app images upload to R2 in the background and fill in `media.r2_key`; remote
   images are already there.
6. **Write** the row(s), mark the job done.

The same content-addressed layout means an image that arrives twice (once from Telegram, once
pasted into the app) is stored once, whichever door got there first.

**Document jobs** (PDFs, uploaded files, `application/pdf` URLs):

1. **Extract the text layer locally** with a pure-Go PDF library; that text feeds FTS5 and the
   embedding.
2. **Detect a scan:** if the text layer is empty or tiny relative to the page count, render the
   first ~10 pages and OCR them with `deepseek/deepseek-v4.1-flash-1017` (image input). Cap the
   pages per document so a 400-page scan cannot run up a bill.
3. **Store the file** content-addressed like any other blob; PDF archives stay on demand.
4. **Write** the media row plus the extracted text, mark the job done.

The cap and the text-vs-scan threshold are config values, not hard-coded guesses.

**Video and podcast jobs** (YouTube and similar, v1):

1. **Fetch the transcript on the potato**, not the Worker — YouTube treats datacenter IPs harshly
   and the potato's residential egress is the better citizen. Extract the video id and request the
   caption track in the preferred language.
2. **Store it** in `transcript_text`, and feed it to FTS5 and the embedding alongside the title and
   description.
3. **If no captions exist**, mark the item `transcript_state = 'unavailable'` and keep it — a
   missing transcript must never fail the save or the enrichment.
4. **Podcasts** are best-effort the same way: use a published transcript if one exists; otherwise
   leave the field empty rather than transcribing audio in v1.

The exact transcript source is a spike (see open-questions G): the caption endpoint is not a
stable public API, so the implementation needs a fallback and must degrade quietly.

Failures: retry **3× with backoff over ~an hour**, then park the job as `failed` with the error
kept. The UI shows a dismissible **"needs attention"** strip with one-tap retry; failures are never
silent. A bad URL must never wedge the queue.

**Cost reality check:** a ~2,000-token page in plus one Jev decision (`typesafe/jev-1.14`), a
DeepSeek-Flash-class chat call (`deepseek/deepseek-v4.1-flash-1017`), and a 1024-dim
`pplx-embed-v1-0.6b` vector still lands well under **$0.001/save** — under a dollar a year at
realistic volume. The expensive subscription tier wasn't paying for inference; it was paying for
the UI and the gesture.

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
  source        TEXT NOT NULL DEFAULT 'app', -- app|telegram|shortcut|quick|import
  import_ref    TEXT NOT NULL DEFAULT '',    -- Raindrop id / bookmark path, for re-import
  state         TEXT NOT NULL DEFAULT 'queued', -- queued|enriching|ready|failed
  transcript_text TEXT NOT NULL DEFAULT '',  -- captions/transcript, feeds FTS5
  transcript_state TEXT NOT NULL DEFAULT '', -- ''|ok|unavailable
  link_status   TEXT NOT NULL DEFAULT 'ok',  -- ok|dead|error (scheduled checker)
  last_checked_at DATETIME,                  -- last archive/link check
  deleted_at    DATETIME,                    -- NULL = live; purged after 30 days
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX items_by_url ON items(url)
  WHERE url IS NOT NULL AND url <> '';

CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
-- A merge leaves the old spelling behind as an alias, so a re-import or a
-- future AI tag maps back to the canonical tag instead of recreating it.
CREATE TABLE tag_aliases (alias TEXT PRIMARY KEY, tag_id INTEGER NOT NULL);
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

-- Smart Spaces: 'auto' is rule-driven, 'saved' is an explicit query,
-- 'manual' is hand-curated (Read later, imported Raindrop collections).
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
  item_id      TEXT PRIMARY KEY,
  r2_key       TEXT,                   -- html / pdf / screenshot objects
  bytes        INTEGER NOT NULL DEFAULT 0,
  content_hash TEXT NOT NULL DEFAULT '', -- detect a changed page on re-archive
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE VIRTUAL TABLE items_fts USING fts5(
  title, summary, content_text, transcript_text, note,
  content='items', content_rowid='rowid'
);

CREATE TABLE item_embeddings (
  item_id      TEXT PRIMARY KEY,
  model        TEXT NOT NULL,        -- e.g. perplexity/pplx-embed-v1-0.6b
  dims         INTEGER NOT NULL,     -- 1024 for pplx-embed-v1-0.6b; stored, never assumed
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
  static binary. `perplexity/pplx-embed-v1-0.6b` returns **unnormalized** 1024-dim vectors, so
  normalize on write (or divide during cosine) — never use inner product on them. Vectors live in
  `item_embeddings.vec` as a `BLOB`; SQLite stays the source of truth, the in-memory slice is a
  cache with a rebuild path.
- Combine keyword + semantic into one ranked list with reciprocal rank fusion. Ranking weights
  are tunable and are the one thing left to fit against real data.
- **Instant search:** results update as you type (~200 ms debounce, minimum 2 characters), with
  filter chips for kind, tag, source, and date. There is no separate search page or submit step.
- Later, maybe: colour/brand/visual search (mymind's "Same Vibe" is cosine similarity over
  embeddings you're already generating).

---

## 9. UI (SvelteKit, embedded)

Five surfaces, no folders and no settings maze:

- **Home** — search box (instant, hybrid, filter chips), Smart Space chips, pending cards, then
  the recent grid. **Masonry layout, explicitly** — variable-height cards (covers, screenshots,
  images, note text), not a uniform grid. Cards show media, kind, title, tags.
- **Multi-select** — long-press or the select button enters a bulk bar: Delete, Tag, Add to Space,
  Refresh. Built for cleaning up an import.
- **Save composer** — URL, note, image upload. Reachable from Home and from the quick-save page on
  the phone. Same endpoint shape as the remote doors, written locally.
- **Item** — media, AI + manual tags, Markdown note (rendered with a preview toggle), summary,
  source, archive link, related items. Every field editable; **Delete** moves it to Recently
  deleted. **Refresh** re-runs metadata/archive.
- **Reader** — extracted article view with select-to-highlight and an annotation field. Highlights
  are also listed on the item.
- **Settings** — version, update check/apply (Warbler pattern), import, tag manager (rename,
  merge, delete), export, storage/mirror status.

Smart Spaces appear as a horizontal row of chips below search; `auto` spaces evaluate live from
their rule, `saved` ones open a query, `manual` ones (Read later, imported collections) open their
membership. Masonry columns are responsive: 1 on phone, 2–4 on desktop. Colour/brand/visual search
is a later addition, not a v1 surface.

### Visual direction (settled; mockups later)

**Dark only.** No light mode, no theme switcher. The feel is calm editorial — generous
whitespace, image-first masonry, quiet metadata — with hints of the homelab family (night-sky
depth, amber accent) but its own distinct identity, not a Polaris clone. Mockups will be done
separately with Claude; this is direction, not a spec, and it doesn't block plumbing.

**PWA:** installable (manifest, icons, full-screen) and an Android `share_target`, **no service
worker and no offline cache**. The tailnet is assumed up; offline reading is not a v1 promise.

---

## 10. Polaris integration (deferred — not in v1)

The research thread kept circling a `search_bookmarks()` tool for Polaris, but Polaris does not
support MCP servers today and this is not a v1 concern. Parked deliberately: no MCP endpoint, no
read-only SQLite mount, no schema coupling.

If it comes back, the likely shape is a small read-only HTTP/MCP surface owned by LocalDrop, not
Polaris reading the database directly. Until then, the library's job is to be good on its own.

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
  day one, → R2 with **30 daily snapshots**, plus an `rsync` copy to the MacBook Air
  (`100.94.193.102`) when it's online. This is the Vaultwarden pattern, reused. The database
  **and** the media mirror are both required for a real restore. Test the restore.
- **Encryption:** no extra client-side layer. The R2 bucket is private and R2 encrypts at rest;
  the accepted risk is that anyone with bucket credentials can read the library. (Coffer encrypts
  because it is financial data; this is not.)
- **Pre-update snapshot:** `VACUUM INTO data/backups/pre-update.db` through the running container,
  restored automatically if the new image fails its health check.
- **Deletion:** `deleted_at` is a soft delete; the UI exposes **Recently deleted** for 30 days.
  The purge task then deletes the row (cascading media/highlights/tags) and removes local media
  plus the R2 keys — the Worker exposes a token-gated purge route so the potato never needs R2
  credentials for ordinary deletes. A missed R2 delete is caught by the orphan sweep.
- **Disk guardrails:** per-file upload cap (~25 MB), a storage number in Settings, and a warning
  before the SD card gets close. The potato has room, but not infinite room.
- **Export:** one command writes **JSON, Markdown, and Netscape HTML**, plus a `media/` folder and
  manifest so the export is restorable and readable without the app. Data ownership is the point —
  never build a roach motel like mymind's one-way export.
- **Secrets:** per-source write tokens (`telegram`, `shortcut`, `quick`) stored **hashed** in D1,
  so one can be revoked without touching the others. `OPENROUTER_API_KEY` is held by the potato;
  the Worker never needs it.
- **R2 reads:** the bucket stays private. The browser gets a **short-lived presigned URL**
  (signed by the Worker, ~5 minutes) and fetches directly from R2, so archive bytes never pass
  through the potato.
- **Rate limiting:** Workers rate-limit binding on `/save` (a few/min/IP). Treat as
  abuse-dampening, not a hard guarantee.
- **Cost guardrails:** cap LLM tokens per page, cap page bytes, cap archive size; alert if D1/R2
  usage moves.
- **Link checking / re-archive:** a daily scheduler picks a **staggered slice** of older items
  (aged >30 days, capped ~50/day), re-fetches, refreshes the archive/screenshot, and records
  `link_status` and `last_checked_at`. A 404/410 sets `dead` and keeps the last archive; a
  transient error sets `error` and retries later. Re-archive only writes when the archive
  `content_hash` changes, to limit churn.
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
| 5 | Raindrop CSV + Netscape HTML import | The old library moves in; the switch is real | a weekend |
| 6 | Images + documents: Worker intake → R2, local store, vision/OCR, thumbnails, PDF text/scan extraction, mirror-up | Photos, screenshots, and PDFs save and search | a weekend |
| 7 | Video/podcast transcripts on the potato, into FTS5 + embeddings | Video is searchable by what's said | 1 evening + spike |
| 8 | Reader view + highlights + annotations | Reading happens in the app | a weekend |
| 9 | Tags + Smart Spaces: global tag manager (rename/merge/aliases), rules, chips, saved queries, Read later, bulk actions | Browsing by theme, not just search | a weekend |
| 10 | Brute-force vector search (hybrid ranking) — needs Nova's `embed` primitive (#49) | "That article about the thing" | 1 evening |
| 11 | Archive rendering via Worker → R2, then the scheduled re-archive + link checker | Links keep their contents and stay honest | a weekend |
| 12 | Export command, backup restore drill, storage/mirror UI | Trustworthy to rely on daily | ongoing |

Deliberately absent: extension, native apps, sharing, collaboration, import UI, and the Polaris
MCP tool (deferred; Polaris has no MCP support today).

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
- All AI goes through Nova with one OpenRouter key. Nothing runs locally.
- Classification uses Jev (`typesafe/jev-1.14`) through Nova's `decide`; tags/summary/title use
  `deepseek/deepseek-v4.1-flash-1017` through `provider/openrouter`.
- Embeddings use `perplexity/pplx-embed-v1-0.6b` (1024 dims, unnormalized → cosine) through
  Nova's `embed` primitive, which is issue
  [#49](https://github.com/AutumnsGrove/nova/issues/49). LocalDrop is its first consumer.
- Highlight anchors are quote + prefix/suffix against a versioned extraction (see H2).
- Smart Spaces are user-defined rules plus AI suggestions the user can accept (see H3).
- Images are read by the same `deepseek/deepseek-v4.1-flash-1017` model as text enrichment;
  thumbnails (max 1600px) are generated locally with Go's `image` package.
- Home is search + Spaces + pending + a **masonry** recent grid.
- Archiving: single-file HTML + screenshot on every save; PDF on demand.
- Telegram is a dedicated bot; it accepts links, text, photos, documents, and albums.
- Failed jobs retry 3× over ~an hour, then surface in a "needs attention" strip.
- Export is JSON + Markdown + Netscape HTML + a media manifest and files.
- One canonical item per URL; re-saving **merges** (append note, union tags, refresh archive).
- v1 imports **Raindrop CSV + Netscape HTML**; collections/folders become manual Spaces.
- Read later is an optional **manual collection**, not a read/unread state or queue.
- Deleting is soft for **30 days** (Recently deleted), then DB + media + R2 are purged together.
- Notes are **Markdown**; title, note, tags, kind, and Read later are all editable.
- Kinds are the 12: article, product, book, recipe, video, tweet, note, image, paper, podcast,
  place, movie. `article` is the catch-all for written pages and unknown links.
- Search is instant and hybrid (FTS5 + cosine, RRF) with kind/tag/source/date filter chips.
- Bulk actions: delete, tag, add to Space, refresh.
- PDFs get local text extraction; scans OCR the first ~10 pages via DeepSeek, with a per-document
  cap.
- Tags are normalized on write (case/whitespace + reuse of existing canonical tags); merge leaves
  an alias behind, so re-imports don't resurrect the old spelling.
- UI is **dark-only**, calm editorial + homelab hints, with its own identity; mockups come later.
- PWA is installable with a share target; no service worker, no offline cache.
- No extra backup encryption: private R2 bucket + server-side encryption is the accepted risk.
- Video/podcast transcripts are v1, fetched on the potato, best-effort, never allowed to fail a
  save.
- Link rot is fought with a staggered scheduled re-archive + checker (~50/day, items >30 days),
  not a crawler; `content_hash` limits churn.
- The Polaris MCP tool is **deferred out of v1**. Polaris does not support MCP servers today; no
  read path is built for it.
- Remote writes authenticate with per-source bearer tokens, hashed in D1, revocable one at a time.
- Backups: nightly `VACUUM INTO` → zstd → R2 (30 daily snapshots) + rsync to the MacBook Air;
  media mirrors continuously to R2.

**Also settled**

- Go for the potato service; SvelteKit embedded (Python loses on deployment and memory).
- Port **8895** (8893 Cairn, 8894 Coffer already reserved); confirm free at install.
- Notes and links share one `items` table; images are `items` with `media` rows.
- Smart Spaces evaluate live from stored rules (`auto`), plus explicit saved queries (`saved`) and
  hand-curated `manual` collections; `space_items` is the membership table.
- Highlights anchor with a text-quote selector (quote + prefix/suffix + ordinal + text hash), not
  DOM paths, so they can re-anchor after re-extraction.
- R2 stays private; the browser gets short-lived presigned URLs.

**Still open**

- The **name** (LocalDrop is a placeholder).
- Hybrid ranking weights (keyword vs semantic) — tune once there is real search data.
- The five spikes in [open-questions.md](open-questions.md#g-things-to-verify-with-a-spike-not-decisions)
  (streaming upload, D1 claim, vector cache, Browser Rendering, Nova embed).

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
  [Workers limits](https://developers.cloudflare.com/workers/platform/limits/)
- [Jev on OpenRouter](https://openrouter.ai/docs/guides/community/jev) ·
  [DeepSeek V4.1 Flash 1017](https://openrouter.ai/deepseek/deepseek-v4.1-flash-1017) ·
  [Perplexity embeddings](https://docs.perplexity.ai/docs/embeddings/quickstart)
- Full cited list: [research-thread.md](research-thread.md)
