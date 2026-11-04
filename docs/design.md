# Harbor — design

Distilled from [the research thread](research-thread.md), 2026-11-03. This is the *current best
shape*, not a spec. Anything marked **OPEN** is genuinely undecided — see
[open-questions.md](open-questions.md).

---

## 1. The concept

One inbox for links, notes, images, quotes — saved in one motion, never filed by hand. AI figures
out what each item is and organizes it. Search is associative, not folder-shaped.

The product is **the save gesture**, not the library UI. Raindrop is easy; mymind is easier. The
library only has to be good enough to trust and search.

**Non-goals for v1:** browser extension, native mobile app, sharing/collaboration, multi-user,
public profiles. (See §11 for why each is deferred.)

### v1 scope (settled 2026-11-03)

- **Save:** links, notes, **and images**. Images take two paths: in-app saves write to the
  potato's disk first; remote saves (Telegram, Shortcut) are uploaded to R2 by the Worker at
  ingestion, then pulled down to the potato. Either way the potato keeps a local copy and R2 keeps
  a durable copy.
- **Organize:** AI `kind` (12 kinds) + AI tags + **optional manual tags** + **Smart Spaces**,
  with a global tag manager and multi-select bulk actions.
- **Read:** reader view, **highlights, and annotations on highlights**.
- **Front doors:** in-app save composer, Telegram bot, iOS Shortcut (these two are public, via the
  Worker), and two tailnet-only pages — the quick-save page and the Android PWA share target.
- **Import:** Raindrop CSV and Netscape HTML, so the switch from the paid service is real.
- **Read later:** an optional hand-curated collection, not a read/unread state. The library stays
  a library; a queue product is a later version if it's ever wanted.
- **Editing:** Markdown notes, and every field (title, note, tags, kind, Read later) editable.
- **Delete:** Recently deleted for 30 days, then purge.
- **Not:** browser extension, native app, public sharing, collaboration, multi-user. Sharing is a
  deliberately separate, much later project (§10); nothing in v1 is shaped around it.

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

- **Backend:** Go, module `github.com/AutumnsGrove/Harbor`, server under `server/`, entry point
  `server/cmd/harbor`. Static binary, `CGO_ENABLED=0`, distroless final image, non-root.
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
    has landed (`embed`, `embed/openrouter`; issue
    [#49](https://github.com/AutumnsGrove/nova/issues/49) is still open for bookkeeping). Harbor
    is its first consumer and can use `embed.NewRoles` with an `index` role pinned to 1024 dims.
  Enrichment is a pipeline of one-shot calls, not an agent loop. `message.Parts` carries images to
  the vision-capable chat model. Nova is pinned by tag or commit in `go.mod`; if its repo is private,
  CI and the Docker build need `GOPRIVATE` and a read token (spike S11).
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
  harbor:
    build: .
    image: ${HARBOR_IMAGE:-ghcr.io/autumnsgrove/harbor:latest}
    restart: unless-stopped
    env_file: [.env]                    # digest pin + secrets; see below
    user: "${HARBOR_UID:-1000}:${HARBOR_GID:-1000}"
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    tmpfs: ["/tmp"]
    healthcheck:                        # distroless has no curl; the binary checks itself
      test: ["CMD", "/harbor", "healthcheck"]
      interval: 30s
      timeout: 3s
      retries: 3
    ports:
      - "127.0.0.1:${HARBOR_PORT:-8895}:8080"
    volumes:
      - ./data:/data
      - ./update-signal:/update-signal
```

- **Port 8895** (8893 is reserved for Cairn, 8894 for Coffer) — loopback only; `tailscale serve` is
  the only way in. Confirm it's free with `ss -ltn` before first install.
- **Data:** `~/Harbor/data/` — `harbor.db` (+ WAL), `archives/` if anything is ever stored
  locally, `backups/` for the pre-update snapshot.
- **Deployment mode:** `HARBOR_DEPLOYMENT=docker`, surfaced in Settings.
- **`.env`** (mode 600, never committed) carries: `HARBOR_IMAGE` (the digest pin the watcher
  rewrites — it must preserve every other line), `HARBOR_UID/GID/PORT/DEPLOYMENT`,
  `HARBOR_ALLOWED_LOGIN` (the one Tailscale login), `HARBOR_ALLOWED_HOSTS`, `HARBOR_WORKER_URL`,
  `HARBOR_WORKER_TOKEN` (the `potato` credential), and `OPENROUTER_API_KEY`.
- **One binary, several subcommands** (`harbor serve|healthcheck|snapshot|import|export|backup|
  restore|replay`). The image is distroless, so CLI work runs as
  `docker compose run --rm harbor <subcommand>` (container stopped) or
  `docker compose exec harbor /harbor <subcommand>` (running).

### The update path (inherited, not reinvented)

This is the Warbler pattern, copied deliberately so there is one thing to understand across the
homelab:

```
Settings ──[Check]──► update-signal/check
                            │  harbor-update.path sees the file
                            ▼
                    harbor-update.service (oneshot)
                            │  resolve :latest -> digest
                            ▼
                    update-signal/available.json
Settings ──[Update]─► update-signal/requested (contents: "latest")
Meridian tap ───────► systemctl start harbor-update.service  (no file)
                            │
                            ▼
                    1. pin the new digest in .env
                    2. `harbor snapshot --pre-update` (VACUUM INTO + integrity check)
                    3. pull + recreate
                    4. poll /healthz for up to 120s
                    5. healthy -> result "ok"; otherwise restore snapshot,
                       re-pin the old digest, recreate, result "failed"
```

- The app **never names an image**. It writes `requested` with the literal `latest`; the watcher
  owns the allowed repository and accepts only `<repo>@sha256:<64 hex>`.
- A `harbor-tick.timer` every 10 minutes refreshes a heartbeat and catches missed signals. It
  never updates on its own.
- Endpoints: `GET /healthz` (public to Docker/Meridian, two fields), `GET /api/version`,
  `POST /api/update/check`, `GET /api/update/available`, `POST /api/update/start`,
  `GET /api/update/status`, `GET /api/busy`.
- Meridian target: `kind: compose`, `dir: /home/autumn/Harbor`, `service: harbor`,
  `update: { kind: unit, unit: harbor-update.service }`.
- Full tile, vital, allowlist, observed-unit, and homelab-repo checklist:
  [docs/meridian.md](meridian.md). Do not wire any of it before `/healthz` exists on the box.
- The watcher never updates `docker-compose.yml`, the units, or the script themselves — those come
  from the checkout on the potato (`cd ~/Harbor && git pull && deploy/install.sh`).
- Rollback matters more here than for Warbler: SQLite migrations only go forward, so the snapshot
  restore is not optional.
- A rollback (like any restore) drops items created since the snapshot. Remote saves in that gap are
  recovered by `harbor replay --since <snapshot time>`, which re-opens recent *done* Worker jobs
  (§4 job lifecycle, §11). In-app saves made inside the rollback window are the accepted loss.

### Access control

Same four layers as Warbler: tailnet → `Host` check → Tailscale identity lock
(`Tailscale-User-Login`) → app (no CORS, strict CSP, bundled assets, non-root, read-only rootfs).

**How the lock really works.** `tailscale serve` proxies from 127.0.0.1 and *adds* the identity
header, and inside Docker a published port makes the peer address the bridge gateway, so "is the
peer loopback" cannot tell the tailnet from the host. The rule is therefore header-based: every
request needs `Tailscale-User-Login` equal to `HARBOR_ALLOWED_LOGIN`, with exactly two exemptions —
`GET /healthz` and `GET /api/status`, which are exempt only on a path match **and** only when the
header is absent (host-local callers: Docker, Meridian). Counts only; neither exposes content.

**Egress (a deliberate difference from Warbler).** Harbor must reach `openrouter.ai`, the Worker,
and — because fetching saved pages is its job — arbitrary public sites. A hostname allowlist is
therefore impossible. The controls are (1) the in-app SSRF guard, enforced at dial time, which
refuses loopback, RFC1918, link-local, the Tailscale CGNAT range `100.64.0.0/10`, the Tailscale
ULA `fd7a:115c:a1e0::/48`, `*.ts.net`, `0.0.0.0` and non-80/443 ports; and (2) a host firewall rule
(`DOCKER-USER`) denying the container those same ranges, so a bug in (1) cannot reach the tailnet.

### What CI does

On every push: `gofmt`, `go vet`, `go test -race`, pnpm check/test/build, the no-outside-addresses
frontend check. On a push to `main` that passes: build `linux/amd64` + `linux/arm64`, publish
`ghcr.io/autumnsgrove/harbor` tagged `latest` and the short SHA. The potato only ever pulls; it
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
│ done jobs kept (replay)  │  /jobs   │ image files (local copy)     │
│ R2: intake + mirror      │          │ HTML parsing                 │
│                          │          │ OpenRouter enrichment        │
│                          │          │ (Polaris tool deferred)      │
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
| Owns | `POST /save` + `/upload`, D1 queue (done jobs retained), R2 intake/mirror, Browser Rendering | SQLite + FTS5 + in-process vectors, local image files, HTML parsing, OpenRouter calls, embeddings, UI |
| Can read library | **never** | yes |
| Reachable from | public internet | outbound only |
| Fails when | almost never | fan dies, reboot, power cut |
| At risk | queue entries + unclaimed R2 blobs | the library itself — database **and** local images |
| Cost | $0 on free tier | $0 |

**Security property:** a public **door** token can only append. A leaked `shortcut` token means
someone can add junk — not read, not claim, not presign, not delete, not enumerate. That holds
because tokens carry a **scope** (below): door tokens can call `/save` and `/upload` and nothing
else, and the claim/read/delete surface answers only to the separate `potato` token, which never
leaves the potato. Scope is enforced in the Worker and tested for every route.

**The asymmetry to respect:** the library's meaning lives on the potato — the database is the one
thing that cannot be rebuilt. R2 holds durable copies of blobs (and the intake copy of every remote
save), but a bucket full of orphaned images is not a library. Back up the database and the media
mirror; don't over-engineer the Worker.

### Worker API and token scopes (settled 2026-11-04)

One table, so no phase has to discover a route on its own. Scope `door` = `telegram`, `shortcut`
(public); scope `potato` = the one credential held by the potato. A door token on a `potato`
route is a 403 with no side effects.

| Route | Scope | Phase | Purpose |
|---|---|---|---|
| `POST /save` | door | 1 | enqueue a link/note job |
| `POST /upload` | door | 1 | stream one file to R2, enqueue its job |
| `POST /telegram/webhook` | Telegram secret header **and** owner id | 4 | Telegram updates → jobs |
| `GET /healthz` | none | 1 | `{status, version}`, no D1 read |
| `POST /jobs/claim?limit=N&kinds=` | potato | 1 | atomically claim up to N oldest claimable jobs; `kinds` (link,note,upload) lets the potato claim only what it can process yet |
| `GET /jobs/pending` | potato | 1 | queued + claimed jobs, **read-only** — feeds the pending cards |
| `POST /jobs/:id/done` / `/failed` | potato | 1 | close a job (idempotent) |
| `GET /jobs/replay?since=` | potato | 1 | recent *done* jobs, to re-ingest after a rollback/restore |
| `GET /presign?key=` | potato | 1 | short-lived S3-style GET URL for one known key |
| `PUT /mirror/<key>` | potato | 6 | upload an in-app blob (`media/…`) — the potato holds no R2 credentials |
| `DELETE /objects` | potato | 9 | purge: delete R2 keys for hard-deleted items |
| `POST /render` | potato | 11 | render HTML / screenshot / PDF for a URL into R2 |
| `PUT /backups/<name>` (+ list, delete) | potato | 12 | nightly snapshot upload, retention |
| `GET/POST /admin/tokens…` | potato | 4 | list (no secrets), rotate, revoke door tokens; last-seen per door |

Cron triggers (Worker): lease reclaim every 5 min, daily orphan sweep. Public read surface: none.

---

## 5. Save path

Two ingestion routes on the Worker:

- `POST /save { link?, note?, title? }` → writes a `queued` row in D1 and returns `200` in ~50 ms.
- `POST /upload` (raw body only — no multipart parsing in the Worker — one file per request, so one
  file is one job and one item) → **streams the body straight into R2**
  (`env.BUCKET.put(key, request.body)` — never buffered in the Worker), then writes the D1 job row
  with `r2_key`, `content_type`, and `bytes`. Metadata travels in headers/query
  (`Content-Type`, `X-Harbor-Filename`, optional `?link=`/`?note=`/`?title=`, size-capped).
  `Content-Length` is required (411 without it) because R2 streaming wants a known length; spike S1
  confirms. Returns `200` as soon as R2 has the object. The SHA-256 is computed on the potato when it
  pulls the blob down, because hashing a 25 MB stream in a Worker would mean buffering it. Only
  Telegram groups several files into one item (below).

Both are token-gated and rate-limited, with a content-type allowlist and a hard size cap. A remote
save is "accepted" the moment its blobs are in R2 and the job row exists — the potato catching up
is enrichment, not acceptance.

Front doors (settled):

1. **In-app save composer** — paste a URL, write a note, upload images. The UI already runs on the
   potato, so this writes to the local database and enters the same enrichment queue directly; it
   does not round-trip through the Worker.
2. **Telegram bot** — a **dedicated bot** (own token, Worker webhook, isolated from Mira). Accepts
   links, text notes, photos, documents, and albums; albums group by `(chat_id, media_group_id)`.
   The best mobile gesture, and the only door that handles "save this thought" as naturally as
   "save this URL." Only the owner is served: the Worker checks the sender against
   `TELEGRAM_OWNER_ID` before any D1 write, and drops everyone else with a 200 (a bot username is
   public). Bot API downloads cap at 20 MB, so that — not 25 MB — is Telegram's effective file limit
   and the rejection reply says so.
3. **iOS Shortcut** — the share sheet → `POST /save` on the Worker with a `shortcut` bearer token.
   The only share-sheet door that works when the potato is asleep or the VPN is off.
4. **Tailnet pages** — the **quick-save page** (one input box on the phone's home screen) and the
   **Android PWA `share_target`**. Both post to the potato's own `POST /api/items`, so both need
   the VPN, carry no token, and work with the Worker unreachable. A web manifest `share_target` is a
   plain form POST that cannot attach a bearer header, and the PWA is served from the tailnet
   origin, so it can never be a public door; iOS has no `share_target` at all, which is exactly why
   the Shortcut exists. Saves are attributed `quick` and `share` respectively.

Considered and dropped: floccus (it targets WebDAV/Nextcloud, not arbitrary JSON), the browser
bookmarklet, email-to-save, a Mac launcher/Raycast hotkey, and a browser extension (Manifest V3 +
store review + breaking on Chrome updates is the single biggest reason not to build this
yourself).

### Re-saving is a merge, not a duplicate

One canonical item per canonical URL. Saving an existing URL:

- appends the new note (separated, timestamped), unions tags, keeps Read later state,
- refreshes metadata, archive, and screenshot,
- bumps `saved_at` (the last-saved time, which the home grid sorts on) so the item resurfaces;
  `created_at` keeps the original date and `updated_at` tracks edits.

Canonicalisation lowercases the host, strips the fragment and common tracking parameters
(`utm_*`, `fbclid`), and keeps the rest of the query. A manual **Refresh** action on an item runs
the same path without adding a note. `items.url` carries a partial unique index that deliberately
includes soft-deleted rows, so re-saving a URL that sits in Recently deleted **restores that item
and merges** into it instead of creating a second one. The pre-canonicalisation URL is kept in
`items.url_original`, so a later fix to the canonicalisation rules can recompute keys.

### Import

`Settings → Import` (and a CLI for big files) accepts **Raindrop CSV** and **Netscape HTML**:

- CSV: title, note, excerpt, URL, tags, created date, cover, and highlights are preserved
  (Raindrop's own `id` becomes `import_ref`; `favorite` is reported, not imported — the exact column
  set is confirmed against a real export in spike S9); Raindrop collections become **manual
  Spaces**. Imported items get `saved_at = created_at`, so they sit at their historical position
  instead of flooding "recent".
- HTML: URL, title, and folder; folders become manual Spaces.
- Import is idempotent by canonical URL and merges like a re-save, so running it twice is safe.
- Imported items enter the normal enrichment queue — no special pipeline.
- **No mymind importer in v1.** Their export is `cards.csv` plus files and desktop-only; Raindrop
  and Netscape HTML cover the real library. Revisit only if an actual mymind export lands on disk.

### The pending window

Remote doors have a gap between "save accepted" and "item in the library" (poll interval, or
longer if the potato was asleep). The blob is already safe in R2 during that gap, so the pending
card can even show it from a short-lived signed URL. In-app saves skip the gap by writing locally. For the rest, this must
be visible, or the tool loses trust:

- Settled: the potato calls the Worker's read-only `GET /jobs/pending` (queued + claimed, no side
  effects) and the UI renders those as greyed-out cards at the top. Opening the UI triggers an
  immediate refresh; otherwise it follows the 30 s loop. Claiming is a separate call
  (`POST /jobs/claim?limit=N`) and the loop **drains** — it keeps claiming until the queue is empty —
  so ten Telegram saves are ten items in seconds, not five minutes.

Do not skip this. "I saved it and it vanished" is the failure that kills adoption.

### Job lifecycle (settled 2026-11-04)

Two queues exist and they must not be confused: the **Worker's job queue** (delivery) and the
**potato's enrichment queue** (`items.state`).

1. **Ingest** (fast, never touches a model): claim → pull blobs from R2 (hash while streaming) →
   canonicalise → insert or merge the item, record blobs, set `state='queued'`, and **only then**
   `POST /jobs/:id/done`. The job is done when the save is durable on the potato — not when
   enrichment finishes. (Marking done after enrichment would let the 10-minute lease expire during
   the hour-long retry window and double-process the job.)
2. **Enrich** (slow): driven entirely by the item row — `state`, `attempts`, `next_attempt_at`,
   `last_error`, and `priority` (interactive saves outrank an import backlog). Ingest and enrichment
   run as separate stages so a slow model call never delays the pending→item transition.
3. **The Worker keeps done jobs** (and their `incoming/` blobs) — never hard-deleted by the Worker.
   After a rollback or restore, `harbor replay --since <time>` re-ingests them; ingest is
   idempotent by canonical URL and sha256, so replay is always safe.
4. **Failure is two different things.** A job the potato cannot ingest (bad envelope, missing blob)
   is reported `failed` with `attempts` incremented, and after 4 attempts is parked and surfaced.
   An item whose *enrichment* fails keeps its job done and retries 3× over ~an hour on the item.

---

## 6. Enrichment pipeline

Enrichment is one-at-a-time, with a `sleep`, on purpose: concurrency is what turns a 60 MB spike
into a 300 MB OOM on this box. It is a *separate stage* from ingest (§5 job lifecycle), ordered by
`priority` then `next_attempt_at`, so a freshly saved link is never stuck behind an import backlog.

Per job:

1. **Fetch** the URL through the SSRF guard (§3 access control) with a `Content-Length` cap
   (~5 MB) and a timeout. Stream to a temp file.
2. **Parse from disk** with `go-readability` + `goquery` so peak RSS stays flat. A 4 MB HTML doc
   parsed into a DOM is 40–80 MB resident — the only real memory spike in the system. Store the
   readable text (`content_text`) and Markdown (`content_md`, what the reader renders), and parse
   JSON-LD/microdata (schema.org Recipe, Product, Book, Movie, Place, VideoObject,
   ScholarlyArticle) into `details_json` — the data behind the per-kind pages. An LLM extraction
   is a fallback only for a kind that proves it needs one.
3. **Ask the Worker to render** (asynchronously; the result is recorded when it arrives and never
   gates steps 4–7). Every save gets a single-file **HTML** archive and a **screenshot** (used as
   the card); **PDF is on demand**. Bytes go to R2 and the potato stores the R2 keys. Rendered
   archives are the one blob type that skips local storage.
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

**Items with no URL** (typed notes) skip fetch, extract, render and classification: `kind` is
`note`, and they still get tags, a summary and an embedding. **Image items** take the image path
below, where the vision call supplies the kind.

**Image jobs** (Telegram photo, upload, pasted image) take a different path, and which direction
they travel depends on where they started:

1. **Get the bytes.** Remote images are already in R2 — the claimed job carries `r2_key`; the
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

Failures: retry **3× with backoff over ~an hour** — tracked on the item (`attempts`,
`next_attempt_at`, `last_error`), not on the Worker job — then park the item as `failed` with the
error kept. The UI shows a dismissible **"needs attention"** strip with one-tap retry; failures are
never silent. A bad URL must never wedge the queue.

**Cost reality check:** a ~2,000-token page in plus one Jev decision (`typesafe/jev-1.14`), a
DeepSeek-Flash-class chat call (`deepseek/deepseek-v4.1-flash-1017`), and a 1024-dim
`pplx-embed-v1-0.6b` vector still lands well under **$0.001/save** — under a dollar a year at
realistic volume. The expensive subscription tier wasn't paying for inference; it was paying for
the UI and the gesture.

---

## 7. Data model (sketch — **OPEN** in details)

```sql
-- Migration 0001 creates every table below (schema reconciled 2026-11-04). Later phases only ALTER
-- to add a column, and the phase plan says so. Run with PRAGMA foreign_keys=ON.
CREATE TABLE items (
  seq             INTEGER PRIMARY KEY AUTOINCREMENT, -- FTS5 rowid; AUTOINCREMENT = never reused
  id              TEXT NOT NULL UNIQUE,              -- the public id (uuid)
  url             TEXT,                              -- canonicalized; NULL for pure notes
  url_original    TEXT NOT NULL DEFAULT '',          -- exactly what was saved, pre-canonicalisation
  title           TEXT NOT NULL DEFAULT '',
  note            TEXT NOT NULL DEFAULT '',          -- the user's own words (Markdown)
  kind            TEXT NOT NULL DEFAULT 'article',   -- one of the 12 kinds; never 'unknown'
                                                     -- (URL-less notes are written as 'note')
  kind_source     TEXT NOT NULL DEFAULT 'default',   -- default|ai|manual; manual is never overwritten
  summary         TEXT NOT NULL DEFAULT '',
  content_text    TEXT NOT NULL DEFAULT '',          -- extracted plain text, feeds FTS5
  content_md      TEXT NOT NULL DEFAULT '',          -- extracted Markdown; the reader renders this
  details_json    TEXT NOT NULL DEFAULT '{}',        -- per-kind fields from JSON-LD (recipe, product, place…)
  og_json         TEXT NOT NULL DEFAULT '{}',        -- cover, description, site, author, published, excerpt
  tags_text       TEXT NOT NULL DEFAULT '',          -- denormalised tag names, for FTS5 only
  source          TEXT NOT NULL DEFAULT 'app',       -- app|telegram|shortcut|quick|share|import
  import_ref      TEXT NOT NULL DEFAULT '',          -- Raindrop id / bookmark path, for re-import
  -- enrichment queue (the potato's queue; the Worker's job is already done by now)
  state           TEXT NOT NULL DEFAULT 'queued',    -- queued|enriching|ready|failed
  priority        INTEGER NOT NULL DEFAULT 0,        -- 0 interactive save; -1 bulk/import backlog
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at DATETIME,
  last_error      TEXT NOT NULL DEFAULT '',
  prompt_version  TEXT NOT NULL DEFAULT '',
  extract_version TEXT NOT NULL DEFAULT '',          -- ties to highlights.anchor_json.text_hash
  extract_error   TEXT NOT NULL DEFAULT '',
  enriched_at     DATETIME,
  -- transcripts
  transcript_text  TEXT NOT NULL DEFAULT '',         -- captions/transcript, feeds FTS5
  transcript_state TEXT NOT NULL DEFAULT '',         -- ''|ok|unavailable|error|too_large
  transcript_lang  TEXT NOT NULL DEFAULT '',
  transcript_fetched_at DATETIME,
  -- link health
  link_status     TEXT NOT NULL DEFAULT 'ok',        -- ok|dead|error (scheduled checker)
  last_checked_at DATETIME,
  deleted_at      DATETIME,                          -- NULL = live; purged after 30 days
  -- three clocks: when it was made, when it was last saved, when it was last edited
  created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, -- original date (import preserves it)
  saved_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, -- last save; re-save bumps; home sorts on this
  updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP  -- last edit
);
-- Includes soft-deleted rows on purpose: re-saving a deleted URL restores + merges (§5).
CREATE UNIQUE INDEX items_by_url ON items(url) WHERE url IS NOT NULL AND url <> '';
CREATE INDEX items_by_saved ON items(saved_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX items_queue ON items(state, priority DESC, next_attempt_at);

CREATE TABLE tags (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE);
-- A merge leaves the old spelling behind as an alias, so a re-import or a
-- future AI tag maps back to the canonical tag instead of recreating it.
CREATE TABLE tag_aliases (
  alias  TEXT PRIMARY KEY,
  tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE
);
CREATE TABLE item_tags (
  item_id TEXT    NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
  source  TEXT    NOT NULL DEFAULT 'ai',  -- ai|manual
  PRIMARY KEY (item_id, tag_id)
);
CREATE INDEX item_tags_by_tag ON item_tags(tag_id);

-- Originals live on the potato; R2 is a mirror. One row per item attachment; the file is shared by sha256.
CREATE TABLE media (
  id            TEXT PRIMARY KEY,
  item_id       TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL,              -- image|pdf|file (thumbnails are derived files, not rows)
  filename      TEXT NOT NULL DEFAULT '',
  content_type  TEXT NOT NULL DEFAULT '',
  bytes         INTEGER NOT NULL DEFAULT 0,
  sha256        TEXT NOT NULL,              -- content address + mirror integrity
  width         INTEGER,
  height        INTEGER,
  local_path    TEXT NOT NULL,              -- relative to data/media
  r2_key        TEXT NOT NULL DEFAULT '',   -- set once mirrored (or the incoming/ key for remote saves)
  pdf_text_state TEXT NOT NULL DEFAULT '',  -- ''|text|ocr|empty
  derived_at    DATETIME,                   -- thumbnail generation time, for the re-derive sweep
  created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX media_by_item ON media(item_id);
CREATE INDEX media_by_sha ON media(sha256);   -- reference counting before any unlink or R2 delete

-- Reader highlights; anchor is a text-position selector, not a CSS path.
CREATE TABLE highlights (
  id          TEXT PRIMARY KEY,
  item_id     TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  quote       TEXT NOT NULL,
  note        TEXT NOT NULL DEFAULT '',    -- annotation on the highlight
  color       TEXT NOT NULL DEFAULT 'amber', -- amber|magenta|teal|blue
  anchor_json TEXT NOT NULL,               -- quote + prefix/suffix + ordinal + text_hash (+ source)
  detached    INTEGER NOT NULL DEFAULT 0,  -- 1 = could not be re-anchored; shown separately
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX highlights_by_item ON highlights(item_id);

-- Smart Spaces: 'auto' is rule-driven, 'saved' is an explicit query,
-- 'manual' is hand-curated (Read later, imported Raindrop collections).
CREATE TABLE spaces (
  id         TEXT PRIMARY KEY,
  slug       TEXT UNIQUE,                  -- non-NULL only for system spaces; 0001 seeds 'read-later'
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL DEFAULT 'auto',
  rule_json  TEXT NOT NULL DEFAULT '{}',   -- see open-questions H3 for the exact vocabulary
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE space_items (
  space_id TEXT NOT NULL REFERENCES spaces(id) ON DELETE CASCADE,
  item_id  TEXT NOT NULL REFERENCES items(id)  ON DELETE CASCADE,
  PRIMARY KEY (space_id, item_id)
);
CREATE INDEX space_items_by_item ON space_items(item_id);
-- AI suggestions: never become Spaces by themselves; dismissals are remembered by fingerprint.
CREATE TABLE space_suggestions (
  id          TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL UNIQUE,        -- hash of the suggested rule
  label       TEXT NOT NULL,
  rule_json   TEXT NOT NULL,
  state       TEXT NOT NULL DEFAULT 'pending', -- pending|accepted|dismissed
  created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Worker-rendered snapshots (never stored locally; referenced by key). Latest copy only.
CREATE TABLE archives (
  item_id        TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  html_key       TEXT NOT NULL DEFAULT '',
  screenshot_key TEXT NOT NULL DEFAULT '',
  pdf_key        TEXT NOT NULL DEFAULT '',   -- on demand
  bytes          INTEGER NOT NULL DEFAULT 0,
  content_hash   TEXT NOT NULL DEFAULT '',   -- detect a changed page on re-archive
  created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Written explicitly in the same transaction as the item (no triggers). Because it is an external
-- content table, every update/delete must first issue the FTS 'delete' command with the OLD column
-- values, so the store reads the old row inside the transaction.
CREATE VIRTUAL TABLE items_fts USING fts5(
  title, summary, content_text, transcript_text, note, tags_text,
  content='items', content_rowid='seq'
);

CREATE TABLE item_embeddings (
  item_id      TEXT PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
  model        TEXT NOT NULL,        -- e.g. perplexity/pplx-embed-v1-0.6b
  dims         INTEGER NOT NULL,     -- 1024 for pplx-embed-v1-0.6b; stored, never assumed
  content_hash TEXT NOT NULL,        -- skip re-embedding unchanged rows
  vec          BLOB NOT NULL,        -- float32 little-endian; no extension needed
  created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Bookkeeping
CREATE TABLE processed_jobs (        -- Worker job ids already ingested: makes ingest/replay idempotent
  job_id  TEXT PRIMARY KEY,
  item_id TEXT,
  done_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE import_runs (           -- resumable imports; the canonical URL set is the idempotence key
  id TEXT PRIMARY KEY, file_sha256 TEXT NOT NULL, state TEXT NOT NULL,
  checkpoint INTEGER NOT NULL DEFAULT 0, counts_json TEXT NOT NULL DEFAULT '{}',
  started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, finished_at DATETIME
);
CREATE TABLE task_runs (             -- the in-process scheduler's memory, so a reboot never skips a day
  name TEXT PRIMARY KEY, last_run_at DATETIME, last_ok_at DATETIME, last_error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at DATETIME NOT NULL);
```

Migration numbering is **strictly sequential in merge order** (`0001`, `0002`, …), named for the
phase that introduced them (`0003_transcripts.sql`). Phases 4–10 may be built in any order, so a
phase never claims a number in advance.

Still undecided: multiple attachments per item (the table allows it; the UI picks).

Embedding dimension is deliberately not hard-coded: `model` plus `content_hash` per row makes a
model swap detectable and re-embeddable, and the brute-force cache reads whatever is stored.

---

## 8. Search

- **FTS5** for keyword search — built into SQLite, no extra process, good enough for one user.
  Columns: title, summary, content_text, transcript_text, note, and `tags_text` — a denormalised
  copy of the item's tag names on `items`, refreshed in the same transaction whenever a tag is
  added, removed, renamed or merged, so typing "rust" finds items tagged `rust`.
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
  the recent grid (ordered by `saved_at`). **Masonry layout, explicitly** — variable-height cards (covers, screenshots,
  images, note text), not a uniform grid. Cards show media, kind, title, tags. First run shows a
  two-path empty state: **Import Raindrop** or **Paste a first link**.
- **Multi-select** — long-press or the select button enters a bulk bar: Delete, Tag, Add to Space,
  Refresh. Built for cleaning up an import.
- **Save composer** — URL, Markdown note, up to 10 images, and a Read later switch; it warns when the link is already saved and will merge. Reachable from Home and from the quick-save page on
  the phone. Same endpoint shape as the remote doors, written locally.
- **Item** — media, AI + manual tags, Markdown note (rendered with a preview toggle), summary,
  source, archive link, and a quiet **related items** row (3–6 similar items from the embedding
  index). Every field editable; **Delete** moves it to Recently deleted. **Refresh** re-runs
  metadata/archive.
- **Reader** — extracted article view with select-to-highlight and an annotation field. Highlights
  are also listed on the item. **Articles only in v1**; PDFs and archived pages are view-only.
- **Archive viewer** — the HTML archive opens in a **sandboxed viewer with scripts disabled**
  (an isolated R2 origin, no forms, no JS). Reader text is the default; the raw file is also
  downloadable. The app never renders archived HTML unsandboxed.
- **Settings** — version, update check/apply (Warbler pattern), import, tag manager (rename,
  merge, delete), export, and a **Status** page (last poll, queue depth, last backup, mirror lag,
  disk, last link check).

Smart Spaces appear as a horizontal row of chips below search; `auto` spaces evaluate live from
their rule, `saved` ones open a query, `manual` ones (Read later, imported collections) open their
membership. Masonry columns are responsive: 1 on phone, 2–4 on desktop. Colour/brand/visual search
is a later addition, not a v1 surface.

### Visual direction (settled 2026-11-03; mockup in `design/mockup/`)

**Dark only**, with one reference: the clickable mockup in [`design/mockup/`](../design/mockup/README.md).
Three directions were explored (Lantern, Chart Room, Quiet Gallery) and blended: Lantern's top bar,
Space chips and 4-column masonry; Chart Room's teal-black ground and chart magenta; Quiet Gallery's
single grotesque face, borderless cards and details on hover. Phone follows a native two-column
pattern with a floating glass bar.

- **Colour has three jobs.** Magenta = something you can act on (Save, active chip, focus, selection).
  Amber = something waiting on you (pending saves, needs attention, dead links, Read later). Teal =
  healthy or mirrored. Nothing else is coloured. The mockup has a switch that turns amber off.
- **One face**, Schibsted Grotesk, including the reader. No serif, no mono except inline code.
- **No borders.** Separation is surface tone and space; the one hairline is the 7% rule between list rows.
- **Per-kind item pages** for all 12 kinds (recipe ingredients and method, video chapters and transcript,
  product price (current, from JSON-LD; a price-history trend is deferred), paper abstract and text-layer state, place address and hours, and so on).
- **Highlights have four colours** (amber, magenta, teal, blue), stored in `highlights.color`.
- **Dead links and paywalls are badges, not errors**: a card gets an amber "Link dead" badge and keeps
  its archive; `is_paywalled` shows the same way.
- **Recently deleted lives in Settings**, with Restore and Delete now.
- **Keyboard shortcuts** on desktop: `/` search, `n` new save, `g h` home, `j k` move, `x` select, `?` list.
- **Settings pages:** Tags, Spaces, Import, Recently deleted, Sources and tokens, Status, Export and
  backup, Shortcuts, Design system.
- Tokens: [`design/mockup/css/tokens.css`](../design/mockup/css/tokens.css) is the source of truth.

**PWA:** installable (manifest, icons, full-screen) and an Android `share_target` — which is
tailnet-only (it posts to the potato; iOS has no `share_target`, the Shortcut covers iOS) — with
**no service worker and no offline cache**. The tailnet is assumed up; offline reading is not a v1 promise.

---

## 10. Deferred integrations (not in v1)

### Sharing — much later, and a separate project

Public sharing is not scoped into the core buildout. When it arrives it will be a **separate
Cloudflare Worker and R2 bucket dedicated to shared snapshots**: the potato pushes explicitly chosen
items to it (the secret lives only on the potato), the Worker serves unlisted links, and revoking
deletes the prefix. This is the same pattern as pixelsort-go's share Worker
(`pixelsort-go/PLAN.md` §6b). Consequences for now: the intake Worker described in §4 has no read
route and must never grow one, and the data model needs nothing extra. Revisit when it is wanted.

### Polaris integration

The research thread kept circling a `search_bookmarks()` tool for Polaris, but Polaris does not
support MCP servers today and this is not a v1 concern. Parked deliberately: no MCP endpoint, no
read-only SQLite mount, no schema coupling.

If it comes back, the likely shape is a small read-only HTTP/MCP surface owned by Harbor, not
Polaris reading the database directly. Until then, the library's job is to be good on its own.

---

## 11. Operations

- **Blob layout:** local originals are content-addressed at `data/media/<sha256[0:2]>/<sha256>`,
  with generated thumbnails alongside. Content addressing gives dedupe and a hash to verify every
  copy. Thumbnails are derived and rebuildable; originals are not.
- **R2 key schemes:** `incoming/<job-id>/<n>` (remote intake), `media/<sha[0:2]>/<sha>` (in-app
  mirror), `archives/<item-id>/<html|screenshot|pdf>`, `backups/<date>.db.zst`. The same bytes
  arriving by both doors briefly occupy two R2 objects; every R2 delete (purge, supersede) is
  reference-counted by key *and* by `media.sha256`, exactly like local unlinks.
- **R2 intake keys:** the Worker streams uploads to `incoming/<job-id>/<n>`, because it cannot hash
  a streamed body before writing it. The potato hashes on pull and records the real SHA-256; the
  R2 object is left where it landed and referenced by `media.r2_key`. (A later normalisation pass
  could copy it to a content-addressed key, but nothing depends on that.)
- **Mirror directions:** remote saves go **in** via the Worker; in-app saves go **up** via an
  idempotent background task that uploads any `media` row with an empty `r2_key` through the Worker's
`PUT /mirror/<key>` (`potato` scope — the potato holds no R2 credentials). Mirror failures
  never block saves; they show as a "not mirrored" count in Settings.
- **Orphan cleanup:** if an R2 upload succeeds but the D1 insert fails, the object is unreferenced.
  A daily Worker cron deletes `incoming/` objects older than 7 days with no matching job row. The
  reverse case — a job row whose object is gone — is a retryable `failed` job, not data loss.
- **Backup:** nightly `VACUUM INTO` (never `.backup` — the online-backup API livelocked against a
  live writer on this box once already; see `homelab/docs/coffer.md`), compressed and tiered from
  day one, → R2 with **30 daily snapshots**, plus an `rsync` copy to the MacBook Air
  (`100.94.193.102`) when it's online — a host-side script, ideally pulled by the Mac, since the Mac
is often asleep and a distroless container has no rsync. This is the Vaultwarden pattern, reused.
Keep only the last 3 snapshots on the potato's SD card (30 live in R2). A snapshot can exceed the
Worker's single-request body limit once embeddings and text pile up, so the backup route accepts a
multipart upload. The database
  **and** the media mirror are both required for a real restore. Test the restore.
- **Encryption:** no extra client-side layer. The R2 bucket is private and R2 encrypts at rest;
  the accepted risk is that anyone with bucket credentials can read the library. (Coffer encrypts
  because it is financial data; this is not.)
- **Pre-update snapshot:** `VACUUM INTO data/backups/pre-update.db` through the running container,
  restored automatically if the new image fails its health check.
- **Deletion:** `deleted_at` is a soft delete; the UI exposes **Recently deleted** for 30 days.
  The purge task then deletes the row (every child table cascades — see the DDL's foreign keys)
  and removes local media plus the R2 keys, each only if no other row still references the sha —
  through the Worker's `DELETE /objects` route (`potato` scope) so the potato never needs R2
  credentials for ordinary deletes. A missed R2 delete is caught by the orphan sweep.
- **Restore:** CLI + documented drill, not a UI button. `docker compose run --rm harbor restore
  <snapshot>` on the potato with the container stopped; Settings only reports the last backup. After
  any restore, `harbor replay --since <snapshot time>` re-ingests the Worker's retained done jobs,
  so the gap between the snapshot and the disaster is refilled for every remote save. The app should never replace
  the database it is currently running on.
- **Archive history:** one archive per item (`archives` PK is `item_id`). Scheduled re-archive
  overwrites only when `content_hash` changes, so there is no version history — latest copy only.
- **Disk guardrails:** per-file upload cap (~25 MB), a storage number in Settings, and a warning
  before the SD card gets close. The potato has room, but not infinite room.
- **Export:** one command writes **JSON, Markdown, and Netscape HTML**, plus a `media/` folder and
  manifest so the export is restorable and readable without the app. Data ownership is the point —
  never build a roach motel like mymind's one-way export.
- **Secrets:** door tokens (`telegram`, `shortcut`) and the `potato` token are stored **hashed** in
  D1 with a `scope`, so one can be revoked or rotated without touching the others. There is no
  `quick` token: the quick-save page is tailnet-only and writes locally. `OPENROUTER_API_KEY` is
  held by the potato. The Worker holds `TELEGRAM_OWNER_ID`, the webhook secret, and a read-only R2
  S3 credential used solely to sign URLs.
- **R2 reads:** the bucket stays private. The Worker signs a **short-lived S3-style presigned GET**
  (~5 minutes, `aws4fetch`, read-only R2 credential scoped to this bucket) for one known key; the
  browser fetches directly from R2, so bytes never pass through the potato **or the Worker** (a
  Worker-served signed URL would be a public read path). The app's CSP allows exactly the R2
  host for `img-src` and `frame-src`, and the no-outside-addresses CI check whitelists it.
- **Scheduling:** one in-process scheduler with its last runs persisted in `task_runs` (so a reboot
  never skips a day) owns purge, mirror drain, link check, suggestions, and maintenance. systemd is
  used only for the update watcher and host-side jobs (rsync).
- **Rate limiting:** Workers rate-limit binding on `/save` (a few/min/IP). Treat as
  abuse-dampening, not a hard guarantee.
- **Cost guardrails:** cap LLM tokens per page, cap page bytes, cap archive size; alert if D1/R2
  usage moves.
- **Link checking / re-archive:** a daily scheduler picks a **staggered slice** of older items
  (aged >30 days, capped ~50/day) and re-fetches them **on the potato** (cheap), hashing the
  extracted text. Only a changed hash asks the Worker to re-render, because Browser Rendering's
  free plan is 10 browser-minutes a day and 50 renders would eat most of it. It records
  `link_status` and `last_checked_at`. A 404/410 sets `dead` and keeps the last archive; a
  transient error sets `error` and retries later. Re-archive only writes when the archive
  `content_hash` changes, to limit churn.
- **Observability:** structured JSON logs to stdout (captured by Docker) plus a Settings **Status**
  page fed by the same counters. No telemetry, no external logging. The status page is the answer
  to "is it actually working?" without reading `docker logs`.
- **Deployment:** Docker Compose alongside the other potato services. `/healthz` stays static and
  two-field; the poll loop's last-success timestamp lives on `/api/status` and the Status page.

---

## 12. Build order

Each phase exits with something usable. Steps 0–4 are already a working link + note manager; the
rest adds the v1 scope without ever breaking the save path.

**The actual task lists, exit criteria, risks, and per-phase tests live in
[docs/plans/](plans/README.md)** — one document per phase, plus [testing.md](plans/testing.md) (TDD
and fuzzing) and [spikes.md](plans/spikes.md) (the twelve verifications that gate a phase). This table
is the summary; it does not drift, because the plans reference it and design.md wins any conflict.

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
| 10 | Brute-force vector search (hybrid ranking) + related items via Nova's `embed` | "That article about the thing", and what sits next to it | 1 evening |
| 11 | Archive rendering via Worker → R2, then the scheduled re-archive + link checker | Links keep their contents and stay honest | a weekend |
| 12 | Export command, `harbor restore` + restore drill, storage/mirror UI | Trustworthy to rely on daily | ongoing |

Deliberately absent: extension, native apps, sharing (a much later, separate share Worker — §10),
collaboration, and the Polaris MCP tool (deferred; Polaris has no MCP support today).

**Build sequence (differs from the table order on purpose).** The numbers are stable ids, not the
order. The sequence that reaches "I can cancel Raindrop" fastest, with the irreplaceable data
protected before it exists, is: **0 → 1 → 2 → 3 → 12a → 5 → 4 → 9**, then 10, 8, 6, 7, 11, 12b.
Phase 12a (snapshot, backup to R2, `harbor restore` and the restore drill, export) lands *before*
the first real import. Details, the milestone, and the cut list are in
[plans/README.md](plans/README.md#build-sequence-and-milestones).

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
- Front doors: in-app composer, Telegram, iOS Shortcut (public), and two tailnet-only pages — the
  quick-save page and the Android PWA share target. No floccus, bookmarklet, email-to-save, or
  extension.
- Public collection sharing: **not in v1** — no public read path exists. If it ever comes, it is a
  separate share Worker the potato pushes chosen items to (§10), never a read route on the intake
  Worker.
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
  [#49](https://github.com/AutumnsGrove/nova/issues/49). Harbor is its first consumer.
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
- UI is **dark-only**; the blended direction (Lantern layout, Chart Room colour, Quiet Gallery feel) is
  built in `design/mockup/` and is the reference for the SvelteKit app.
- The composer takes a URL, a Markdown note and up to **10 images** per save (Telegram albums included).
- Recently deleted lives in Settings. Highlights have four colours. Desktop has keyboard shortcuts.
- PWA is installable with a share target; no service worker, no offline cache.
- No extra backup encryption: private R2 bucket + server-side encryption is the accepted risk.
- Video/podcast transcripts are v1, fetched on the potato, best-effort, never allowed to fail a
  save.
- Link rot is fought with a staggered scheduled re-archive + checker (~50/day, items >30 days),
  not a crawler; `content_hash` limits churn.
- Highlights are **articles only** in v1; PDFs and archives are view-only.
- First run offers Import or a first link; no multi-step wizard.
- Archived HTML renders in a sandboxed, script-disabled viewer; the reader is the default.
- Structured logs + a Settings Status page; no telemetry.
- Archives keep the latest copy only; a changed `content_hash` overwrites, unchanged pages are
  skipped.
- Item pages show 3–6 related items from the embedding index.
- No Mac launcher/quick-save helper; the four chosen doors are enough.
- Restore is a CLI command plus a documented, actually-run drill — never a Settings button.
- No mymind importer in v1; Raindrop CSV + Netscape HTML only.
- The name is **Harbor**. The CNCF Harbor container-registry collision is accepted: this is a
  personal project, not a registry, and the gesture meaning is the point.
- The Polaris MCP tool is **deferred out of v1**. Polaris does not support MCP servers today; no
  read path is built for it.
- Remote writes authenticate with per-source bearer tokens, hashed in D1, revocable one at a time.
  The potato holds its own `potato` token, so it rotates independently of the public doors.
- Claimed jobs carry a **lease** (`claimed_at` + `lease_until`, 10 min) so a potato that dies mid-job
  cannot strand a save; the Worker's cron returns expired claims to `queued` and a job that exhausts
  its attempts becomes `failed` and is surfaced, never retried forever.
- Telegram albums are assembled by `(chat_id, media_group_id)` and held back from claiming for ~10 s,
  because a media group arrives as separate updates with no completion signal. This is the one
  deliberate delay in the pipeline.
- FTS5 rows are written explicitly in the same transaction as the item (no triggers), keyed by a
  surrogate `seq INTEGER PRIMARY KEY AUTOINCREMENT` so rowids are never reused or re-shuffled.
- Duplicate-note merging is a no-op when the incoming note is textually identical to one already
  present; imported highlights default to colour `amber` and `anchor_json.source = 'imported'`.
- Backups: nightly `VACUUM INTO` → zstd → R2 (30 daily snapshots) + rsync to the MacBook Air;
  media mirrors continuously to R2.

**Settled in the 2026-11-04 plan audit**

- Tokens carry a **scope**. Door tokens (`telegram`, `shortcut`) can only append; claim, pending,
  presign, mirror, purge, render, backup and admin answer only to the `potato` token. There is no
  `quick` token. The full route table is in §4.
- A Worker job is **done when the save is durable on the potato** (item row inserted), not when
  enrichment finishes. Enrichment retries live on the item (`attempts`, `next_attempt_at`,
  `last_error`).
- The Worker **keeps done jobs and their blobs**; `harbor replay --since` re-ingests them after a
  rollback or restore. Ingest is idempotent, so replay is always safe.
- The pending cards come from a read-only `GET /jobs/pending`; the loop claims in batches and
  drains the queue each poll.
- Enrichment is a separate stage from ingest and is ordered by `priority`, so interactive saves
  outrank an import backlog.
- The Telegram bot serves only the owner (`TELEGRAM_OWNER_ID`); everyone else is dropped with a 200.
- The Android PWA share target and the quick-save page are **tailnet-only** doors; a manifest form
  POST cannot carry a bearer token. The iOS Shortcut is the one public share-sheet door.
- Identity lock is header-based; `/healthz` and `/api/status` are the only exemptions, and only when
  the header is absent. Egress cannot be hostname-allowlisted (the app fetches arbitrary pages): an
  in-app dial-time SSRF guard plus a host firewall rule deny private, loopback and tailnet ranges.
- Presigned reads are S3-style, signed by the Worker with a read-only R2 credential; the Worker
  never proxies archive bytes.
- `items` has three clocks: `created_at` (original), `saved_at` (last save; home sorts on it),
  `updated_at` (last edit). Imported items get `saved_at = created_at`.
- Re-saving a soft-deleted URL restores and merges. `url_original` keeps the pre-canonical URL.
- Every child table cascades from `items`; `tags_text` puts tags in FTS5; `content_md` backs the
  reader; `details_json` (JSON-LD first) backs the per-kind pages. Price-history trends are deferred.
- URL-less notes are kind `note` and skip classification.
- The link checker fetches and hashes on the potato and asks the Worker to re-render only when the
  hash changed (Browser Rendering free = 10 browser-minutes/day).
- One in-process scheduler (state in `task_runs`); systemd only for the update watcher and host-side
  rsync. CLI work runs through `docker compose run|exec`.
- Build sequence is 0 → 1 → 2 → 3 → 12a → 5 → 4 → 9 → …; backups and the restore drill precede the
  first real import.
- Nova is pinned; `go test -race` runs with `CGO_ENABLED=1` in CI while the shipped binary stays
  `CGO_ENABLED=0`.

**Also settled**

- Go for the potato service; SvelteKit embedded (Python loses on deployment and memory).
- Port **8895** (8893 Cairn, 8894 Coffer already reserved); confirm free at install.
- Notes and links share one `items` table; images are `items` with `media` rows.
- Smart Spaces evaluate live from stored rules (`auto`), plus explicit saved queries (`saved`) and
  hand-curated `manual` collections; `space_items` is the membership table.
- Highlights anchor with a text-quote selector (quote + prefix/suffix + ordinal + text hash), not
  DOM paths, so they can re-anchor after re-extraction.
- R2 stays private; the browser gets short-lived presigned URLs.

### Still open

- Hybrid ranking weights (keyword vs semantic) — tune once there is real search data.
- The implementation spikes in
  [open-questions.md](open-questions.md#g-things-to-verify-with-a-spike-not-decisions) (streaming
  upload, D1 claim, vector cache, Browser Rendering, PDF extraction, port, transcripts) plus the
  four added 2026-11-04: a real Raindrop export sample (S9), Tailscale-serve header and Docker
  source-address behaviour (S10), Nova module access in CI/Docker (S11), and image decode memory
  and EXIF (S12). Nova's `embed` prerequisite has landed, so vector search is no longer blocked
  on Nova.

### How it is built (settled 2026-11-03)

- **Test-driven development.** Every behaviour starts as a failing test; a commit that adds behaviour
  without the test that demanded it is rejected on review. Pure rules and store behaviour are covered
  first, because they are the rules a user notices.
- **Fuzzing on every untrusted-input boundary.** Go's native fuzzer (`testing.F`, corpus committed in
  `testdata/fuzz/`) for URL canonicalisation, HTML/PDF extraction, FTS query building, Telegram
  updates, Space rules, highlight anchors, media keys, and Markdown sanitisation;
  `pgregory.net/rapid` for relational invariants (idempotence, merge commutativity, rule/SQL
  equivalence, import idempotence) and model-based queue tests; `fast-check` on the Worker/Frontend
  side. Full table: [plans/testing.md](plans/testing.md).
- **AI output is asserted for shape, not content.** Schema validity, a kind from the 12, 3–6 tags,
  1024 dims. Quality is tracked with a scored fixture set and reported, never used as a deploy gate.
- **A phase is not done until it is deployed** through the phase-0 update path and its exit demo has
  been run on the potato, from the door a human would actually use.
- **The plans are the plan; design.md is the truth.** If the build disproves a decision here, the
  decision log changes first, then the plan, then the mockup.

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
