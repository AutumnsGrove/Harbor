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
UI, multi-user, public profiles. (See §10 for why each is deferred.)

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

---

## 3. Architecture

### The split: write path vs read path

```
 public internet                          tailnet
┌──────────────────────────┐          ┌──────────────────────────────┐
│ Cloudflare Worker        │          │ the potato                   │
│                          │          │                              │
│ POST /save  (write-only) │          │ web UI (read-write)          │
│ D1: jobs queue           │◄─────────│ poll loop (outbound only)    │
│ R2: archive blobs        │  GET     │ SQLite + FTS5 + sqlite-vec   │
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
| Owns | `POST /save`, D1 queue, R2 blobs, Browser Rendering, embeddings | SQLite + FTS5 + sqlite-vec, HTML parsing, OpenRouter calls, UI |
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

## 4. Save path

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

## 5. Enrichment pipeline

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

## 6. Data model (sketch — **OPEN** in details)

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

CREATE VIRTUAL TABLE item_vec USING vec0(embedding float[768]);
```

Open modelling questions: notes vs items as separate tables, attachments (multi-file), tag
synonyms/merging, source of save (floccus/telegram/shortcut), and whether highlights/annotations
belong in v1.

---

## 7. Search

- **FTS5** for keyword search — built into SQLite, no extra process, good enough for one user.
- **sqlite-vec** for "that article about the thing" associative queries. Pure-C extension, stores
  vectors as ordinary BLOBs, runs happily on a Le Potato.
- Combine into one result list (reciprocal rank fusion or a simple weighted blend). **OPEN:**
  ranking weights.
- Later, maybe: colour/brand/visual search (mymind's "Same Vibe" is cosine similarity over
  embeddings you're already generating).

---

## 8. UI (**OPEN** — deliberately minimal)

- One screen: search box, grid of cards, click to open; reader view for articles.
- Save-status surface: pending cards at the top.
- No folders, no manual sorting, no views toggle, no settings maze.
- Frontend question (Svelte vs server-rendered Go/htmx vs plain) is unresolved — see open
  questions.

---

## 9. Polaris integration (the payoff)

An MCP tool — `search_bookmarks(query, limit?)` — pointed at the potato's SQLite. This is the
reason the project is worth building instead of self-hosting Karakeep: Polaris already has the
agent harness, the MCP pattern, and the tailnet.

Possible surface: `search_bookmarks`, `save_bookmark`, `get_bookmark`, `list_recent`. **OPEN:**
read-only vs read-write, and whether save-by-voice ("Polaris, save this") is in scope.

---

## 10. Operations

- **Backup:** nightly `sqlite3 .backup` → R2 (or litestream-style continuous replication). This
  is the only failure in the system that is actually bad. Test the restore.
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

## 11. Build order

Each step is independently useful. Steps 1–2 alone are a working bookmark manager; everything
after is enrichment that can't break the save path.

| # | Step | Why now | Est. |
|---|---|---|---|
| 1 | Worker: `POST /save` → D1 | Saves work immediately, even with nothing behind them | 1 evening |
| 2 | Potato: poll loop + SQLite schema | Saves actually land | 1 evening |
| 3 | Worker: Browser Rendering endpoint | Archives without loading the potato | a weekend |
| 4 | Potato: OpenRouter enrichment (kind/tags/summary) | The library starts organizing itself | a weekend |
| 5 | FTS5, then sqlite-vec | Search you'd actually use | 1 evening each |
| 6 | MCP tool → Polaris | The payoff | 1 evening |
| 7 | Front doors: floccus, Telegram, Shortcut | Only matters once it's trustworthy | 1 evening each |
| 8 | Minimal UI polish, reader view, pending cards | Daily-use quality | ongoing |

Deliberately absent: extension, native apps, sharing, collaboration, import UI.

---

## 12. Decisions log

**Settled**

- Personal, single-user, tailnet-hosted; no accounts, no billing, no multi-tenancy.
- Public write-only Worker; private read-write potato; potato polls outbound.
- SQLite on the potato + FTS5 + sqlite-vec. No Postgres, no Meilisearch.
- Save path never depends on enrichment succeeding.
- Non-goals: no browser extension, no native mobile app in v1 (PWA if anything).
- Archive blobs in R2; the potato stores keys, not files.
- Cheap remote models for AI; nothing runs locally.
- Polaris MCP integration is the reason to build it.

**Leaning (not locked)**

- floccus + Telegram as the two primary front doors.
- Go for the potato service (single binary, matches Polaris) — vs Python.
- Svelte for the UI — vs server-rendered templates.
- Notes and links share one `items` table.
- Reader view + highlights are v1.5, not v1.
- Public collection sharing: skip entirely.

---

## 13. References

- [Raindrop Pro features](https://help.raindrop.io/premium-features) ·
  [pricing](https://contextbolt.com/blog/raindrop-pricing/)
- [mymind](https://mymind.com/) · [2026 review](https://www.marqly.com/blog/mymind-review-2026)
- [Karakeep](https://github.com/karakeep-app/karakeep) ·
  [Linkwarden](https://github.com/linkwarden/linkwarden) ·
  [linkding](https://github.com/sissbruecker/linkding) ·
  [Shiori](https://github.com/go-shiori/shiori)
- [sqlite-vec](https://github.com/asg017/sqlite-vec) ·
  [monolith](https://github.com/Y2Z/monolith) · [floccus](https://floccus.org/)
- [Cloudflare Browser Rendering pricing](https://developers.cloudflare.com/browser-run/pricing/) ·
  [D1 pricing](https://developers.cloudflare.com/d1/platform/pricing/) ·
  [R2 pricing](https://developers.cloudflare.com/r2/pricing/) ·
  [Workers limits](https://developers.cloudflare.com/workers/platform/limits/) ·
  [Workers AI pricing](https://developers.cloudflare.com/workers-ai/platform/pricing/)
- Full cited list: [research-thread.md](research-thread.md)
