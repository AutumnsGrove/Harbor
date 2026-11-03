# Open questions

The agenda for the next refinement pass. Nothing here blocks starting step 1 (the Worker's
`POST /save`), but most of it blocks step 2 onward.

Format: **question → why it matters → current lean.** Quoted answers get folded into
[design.md](design.md) and checked off here.

---

## A. Product shape

### A1. ~~What is the minimum item type set for v1?~~ — **settled: links, notes, and images**
Images land on the potato's disk first (there is room), content-addressed by SHA-256; R2 is an
asynchronous mirror. Vision + OCR are part of the image pipeline. See design §6–7.

### A2. ~~What does "it organizes itself" mean, concretely?~~ — **settled: all of it**
AI `kind`, AI tags, **optional manual tags**, and **Smart Spaces** as a first-class navigation
surface. The schema now has `tags` with per-link `source` (`ai|manual`), plus `spaces` /
`space_items`. The remaining questions are the rule vocabulary for Spaces (H3) and tag
merging/synonyms.

### A3. ~~Do you actually want to read articles in this thing?~~ — **settled: reader + highlights + annotations**
Full reading surface in v1: extracted article view, select-to-highlight, an annotation on each
highlight, and highlights listed on the item. The hard part is anchoring (H2), not the UI.

### A4. Is the UI one screen forever?
Grid + search + pending cards, or do you want the mymind-style "Top of Mind" / serendipity
surfacing on open?

- **Why:** the landing surface decides what the home screen is.
- **Lean:** recent/pending grid on open for v1; serendipity later, cheap to add once the data is
  there.

### A5. Name.
"LocalDrop" is a placeholder. Name it after the gesture, not the library?

- **Why:** affects repo, package names, MCP tool namespace (`search_bookmarks` is generic, fine).
- **Lean:** decide after the thing works; don't bikeshed now. Candidates to consider later:
  something in the Polaris constellation naming family (Pulsar, Constellation, Fields, Stars) or
  a standalone name.

---

## B. Stack & deployment

### B1. ~~What language/runtime for the potato service?~~ — **settled: Go**
Python was ruled out: it "costs" too much in deployment, memory, and maintenance. Parsing works in
Go — `go-readability` + `goquery`, with `trafilatura`/`selectolax` as reference behaviour to match,
not as a dependency.

### B2. ~~UI: Svelte SPA or server-rendered Go + htmx?~~ — **settled: SvelteKit**
Built to static assets and embedded with `go:embed`, same as Warbler/Polaris/bwmon. One binary, no
Node at runtime, no separate static origin. A small JSON API is fine; it is the same origin and the
same identity lock.

### B3. How is it exposed on the tailnet?
`tailscale serve` on the potato, Caddy in Docker Compose, or a raw port + MagicDNS?

- **Why:** TLS, hostname (`potato:8080` vs `localsave.tailnet.ts.net`), and whether it's
  reachable from the phone's browser without warnings.
- **Lean:** `tailscale serve` for HTTPS + MagicDNS, app in Compose bound to localhost.

### B4. Cloudflare Worker: plain module or Hono?
D1 schema, and the shape of the poll API (`GET /jobs?after=` vs `POST /jobs/claim`).

- **Why:** the poll/claim protocol determines retry semantics and whether two potato instances
  could double-process. Plain module + a few routes is probably enough.
- **Lean:** Hono for routing ergonomics; claim-by-update (one worker, but correctness is free);
  `GET /jobs` returns queued rows including `r2_key` for blobs, `POST /jobs/:id/{done,failed}`
  closes them. `/upload` streams to `incoming/` and creates the row after the object exists.

### B5. ~~Where does the SQLite file live, and how is it deployed?~~ — **settled: Compose + host bind mount**
`~/LocalDrop` on the potato, `docker-compose.yml` at the repo root, `./data:/data`, `./update-signal`
mailbox, loopback port 8895 behind `tailscale serve`, image pinned by digest in `.env`. Warbler's
watcher handles updates. Backup remains open (E2): in-app nightly `VACUUM INTO` → R2, plus a
documented, actually-run restore drill.

---

## C. Enrichment pipeline

### C1. Which model, and what exactly does it return?
Kind + tags + summary + title cleanup? One call or two? JSON schema?

- **Why:** determines cost, reliability, and whether re-enrichment is affordable.
- **Lean:** one cheap model call (Gemini Flash class via OpenRouter), strict JSON schema:
  `{kind, tags[3-6], summary<=2 sentences, title_clean?}`. No free-text parsing. Store the prompt
  version so re-enrichment is possible.

### C2. What gets archived, and who renders it?
Screenshot + PDF + single-file HTML on every save via Browser Rendering, or on demand? Keep an
HTML-only fallback (monolith) if the Worker is down?

- **Why:** archive size vs fidelity vs cost. Also determines what "open archive" means in the UI.
- **Lean:** HTML (single-file) always; screenshot for cards; PDF only on demand. Fall back to
  `monolith`-style local capture when the Worker isn't available.

### C3. Embeddings: Workers AI or OpenRouter, and what dimensions?
Which model, and what triggers re-embedding?

- **Why:** dimension affects the in-memory cache and re-embed cost. Store `model` + `content_hash`
  per row so a model swap is detectable and only changed rows are re-embedded.
- **Settled by stack:** exact brute-force cosine in Go over a process-local slice, no
  `sqlite-vec` (cgo would break the static cross-compiled binary; Polaris set this precedent and
  documented ~50k vectors as the revisit point).
- **Lean:** Workers AI (free tier) for embeddings; cheap OpenRouter model for classification.
  **OPEN:** exact embedding model + dimension.

### C4. Failed jobs: what does the user see?
Retry N times, then park as `failed`; surface failures in the UI, or silently drop after logging?

- **Why:** silent failure erodes trust exactly like the pending-window problem.
- **Lean:** exponential backoff (3 tries over ~1 hour), then `failed` with the error stored; a
  "needs attention" strip in the UI, dismissible per item.

### C5. Poll cadence and pending visibility.
30s always, or only when the UI is open / on a schedule? Does opening the UI force a poll?

- **Why:** the tradeoff is battery/idle wakeups vs how long a save appears to vanish.
- **Lean:** 30s poll loop with jitter; UI load triggers an immediate poll; pending cards rendered
  from the poll response.

---

## D. Front doors

### D1. ~~Is floccus viable?~~ — **dropped**
It targets WebDAV/Nextcloud Bookmarks/Google Drive/local files, not arbitrary JSON. The chosen
front doors are in-app, Telegram, iOS Shortcut / PWA share, and the quick-save page — no WebDAV
surface to maintain.

### D2. Telegram bot: new bot or extend Mira?
Where does the webhook live (Worker), and does it accept photos/files or just links/text?

- **Why:** the bot is the best mobile gesture, and a Worker webhook is ~50 lines. But Mira may
  already own that bot token.
- **Lean:** a dedicated bot + Worker webhook; accept text/URL/photo/forwarded messages; reuse the
  same `POST /save` path internally.

### D3. ~~Is the iOS Shortcut worth it if the PWA share target exists?~~ — **settled: both**
Publish a Shortcut recipe and a PWA manifest `share_target`; both hit the same Worker endpoint.
Remaining detail (H4): what the Shortcut sends for a page with no metadata beyond the URL.

### D4. ~~Email-to-save in v1?~~ — **dropped**
Not one of the chosen doors. It can be added later as another Worker route writing to the same
`jobs` table without touching the rest of the system.

---

## E. Security & operations

### E1. Auth model for the write endpoint?
Single bearer token in a Shortcut/bookmarklet/Telegram config? Per-source tokens? Signed URLs?

- **Why:** a leaked token can only append, but per-source tokens make revocation sane.
- **Lean:** one bearer token per source (`shortcut`, `floccus`, `telegram`), stored as Worker
  secrets/rows; revoke one without breaking the others.

### E2. Backup destination and retention.
R2 nightly snapshot with 30-day retention? Litestream-style continuous? Local copy on the NAS/mini
too?

- **Why:** the database is irreplaceable; remote blobs already have a copy in R2, but in-app
  images exist only on the SD card until the mirror runs.
- **Lean:** nightly `VACUUM INTO` → R2 (30 days) **and** a copy to `mini` if it's always-on; the
  media mirror is continuous/retrying rather than nightly. Restore drill documented and actually
  run once — restoring the DB without the media is not a restore.

### E3. Export format and commitment.
JSON + Markdown + Netscape HTML, one CLI command? Guarantee it never becomes a roach motel.

- **Why:** data ownership was a primary motivation; mymind's one-way export is the cautionary
  tale.
- **Lean:** `localsave export --format {json,md,html}` from day one, tested in CI with a fixture.

### E4. How does the browser reach archived files in R2?
Public bucket, presigned URLs, or proxied through the Worker / potato?

- **Why:** a public bucket makes every archive guessable and leaks the library; presigned URLs need
  the Worker to sign; proxying through the potato burns bandwidth it doesn't have.
- **Lean:** private R2 bucket + short-lived presigned URLs signed by the Worker, fetched by the
  potato on demand and cached briefly in memory. Confirm before building the archive UI.

### E5. ~~Public sharing — confirm it's out.~~ — **settled: skip entirely**
No public read path exists. The Worker stays write-only. Revisit only if sharing becomes a real
need, with a narrow Worker-rendered snapshot rather than exposing the UI.

### E6. Cost/usage guardrails.
Max page bytes, max LLM tokens, max archive size, rate limit on `/save`, and what happens when a
free tier is exceeded?

- **Why:** free tiers are generous, but a pathological page or a leaked token shouldn't generate a
  bill or an OOM.
- **Lean:** 5 MB page cap, 8k input tokens, 20 MB archive cap, 10 saves/min/IP; queue pauses
  instead of erroring.

---

### E7. R2 upload abuse and orphan cleanup
What stops a leaked write token from filling the bucket, and what cleans up objects that never got
a D1 row?

- **Why:** the token is append-only, but append-only storage can still be used as free file
  hosting. R2 free tier is 10 GB; abuse is a bill, not a data breach.
- **Lean:** 25 MB per-object cap, content-type allowlist (images/PDF/text), rate limit per token
  and per IP, `incoming/` prefix with a 7-day orphan sweep, and an alert at a storage threshold.
  **OPEN:** whether uploads should be signed per-source rather than one shared bearer token.

---

## F. Polaris integration

### F1. How does Polaris reach the library?
Direct read of the SQLite file (same host, read-only), or an HTTP API on the potato?

- **Why:** direct file reads are simplest but couple the schemas and risk locking; HTTP is clean
  but needs a service and auth. Polaris runs in Docker on the same potato.
- **Lean:** small read-only HTTP API (`GET /api/search?q=`) mounted read-only into Polaris's
  network; keeps the schema private and survives SQLite/WAL quirks. Alternatively a read-only bind
  mount of the DB if WAL mode allows it safely.

### F2. Which MCP tools, and read-only or read-write?
`search_bookmarks` only? Add `save_bookmark`, `get_bookmark`, `list_recent`?

- **Why:** read-write means Polaris can save things you mention in chat — powerful, but a new
  write path with its own trust questions.
- **Lean:** `search_bookmarks` + `get_bookmark` read-only in v1; `save_bookmark` right after
  (it's the same `POST /save`).

### F3. Does "Polaris, save this" matter?
Voice/chat-initiated saves through the assistant, vs only the dedicated front doors.

- **Why:** it's the most "mymind" gesture of all, and Polaris is already the thing you talk to.
- **Lean:** yes, but only after `save_bookmark` exists; no special-casing needed.

---

## G. Things to verify with a spike (not decisions)

1. **Worker streaming upload** — confirm `env.BUCKET.put(key, request.body)` handles a 25 MB
   body on the free plan without buffering or hitting CPU/time limits, and that R2-then-D1 ordering
   leaves a recoverable state when the second step fails.
2. **Worker → D1 queue claim semantics** — confirm the atomic update pattern works with D1's
   consistency model, and what happens on duplicate delivery.
3. **Brute-force vector cache** — measure RSS and search latency on the potato at a realistic
   library size (10k vectors × 768 dims), and confirm the startup load time is acceptable.
4. **Browser Rendering from a Worker on the free plan** — confirm browser hours accounting and
   whether screenshot/PDF/HTML can be captured in one session.
5. **Polaris read path** — read-only bind mount of a WAL-mode DB from another container: safe, or
   does it need the HTTP API?

---

## H. Opened by the v1 scope answers

### H1. Image pipeline details
Which vision model, what exactly is extracted (OCR + description + tags + dominant colours?), and
what are the size/dimension caps?

- **Why:** vision calls cost more than text calls and OCR quality drives searchability; caps
  protect the SD card.
- **Lean:** one cheap vision-capable model call per image returning
  `{kind, tags, ocr_text, description, colors}`; store a max-1600px thumbnail; original capped at
  25 MB; dedupe by SHA-256 on the potato after pull, before any model call. **OPEN:** whether
  thumbnails are generated on the potato (Go `image`) or by the Worker at intake — on the potato
  keeps the Worker tiny and avoids a second image pipeline.

### H2. Highlight anchoring
Text-quote selector (quote + prefix/suffix + ordinal) vs stored DOM offsets vs a rendered plain-text
index?

- **Why:** articles change; a highlight that silently drifts to the wrong sentence is worse than
  no highlight. This is the riskiest part of the reading feature.
- **Lean:** text-quote selector stored in `anchor_json`, re-anchored against the current extracted
  text; if it fails, show the highlight detached ("couldn't re-anchor") rather than guessing.
  Store the exact extracted text version the highlight was made against.

### H3. Smart Space rule vocabulary
What can a Space be defined by — tags, kind, date range, source, text query, similarity to an
item? Who creates them: you, the AI, or both?

- **Why:** the rule schema is user-facing and hard to change later without migrating saved spaces.
- **Lean:** rules = `{tags_all, tags_any, kinds, sources, after, before, text, similar_to_item}`;
  `auto` spaces evaluate live; `saved` spaces are explicit queries. AI may *suggest* a Space, but
  you name and keep it.

### H4. What the in-app composer can save
URL, note, and image uploads — but does it also support pasted rich text, quotes, or multiple
images in one save?

- **Why:** it is the one door you control completely; scope creep here is easy.
- **Lean:** URL + note + up to N images per save (N=4), one `items` row. Quotes are just a note
  with `kind=quote`.

### H5. Telegram media handling
Photos only, or documents/PDFs/albums too? What becomes the title when a photo has no caption?

- **Why:** Telegram albums arrive as separate updates; handling them properly needs grouping.
- **Lean:** photos, documents, and text; album grouping by `media_group_id`; title falls back to
  the AI description, then the filename.

---
