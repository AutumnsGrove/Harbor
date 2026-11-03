# Open questions → decisions

The refinement agenda, worked through on 2026-11-03. Almost everything is now settled; the
answers are folded into [design.md](design.md) and recorded here with the reasoning that produced
them.

**Still open:** hybrid ranking weights and the spikes in section G. Everything else is decided.

Format: ~~question~~ → **decision** → why the alternatives lost.

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

### A4. ~~Is the UI one screen forever?~~ — **settled: Spaces + recent grid, masonry**
Home is search, Smart Space chips, pending cards, then the recent items in a **masonry** layout
(variable-height cards, responsive columns). Top of Mind / serendipity is a later addition, not
v1.

### A5. ~~Name~~ — **settled: Harbor**
Gesture-family name, chosen now: a harbor is where you bring things in and keep them safe.
`Harbor` is the repo, `harbor` the binary/service/image, and `github.com/AutumnsGrove/Harbor` the
module. Known collision: CNCF Harbor is a container registry; accepted deliberately for a personal
project.

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

### B3. ~~How is it exposed on the tailnet?~~ — **settled: `tailscale serve`**
HTTPS + MagicDNS, app bound to `127.0.0.1:8895` in Compose. Confirm the port is free
(`ss -ltn`) at install; 8893 Cairn and 8894 Coffer are already reserved.

### B4. ~~Worker framework and poll protocol~~ — **settled: Hono + claim-by-update**
- Hono for routing.
- `POST /jobs/claim` atomically claims the oldest queued job (status → `claimed`) and returns it
  with `r2_key` for any blobs; `POST /jobs/:id/{done,failed}` closes it. Claim-by-update means
  duplicate delivery can't double-process.
- `/save` and `/upload` create rows; `/upload` streams to `incoming/` first, then inserts.
- `/presign` signs short-lived R2 reads for the UI.

### B5. ~~Where does the SQLite file live, and how is it deployed?~~ — **settled: Compose + host bind mount**
`~/Harbor` on the potato, `docker-compose.yml` at the repo root, `./data:/data`, `./update-signal`
mailbox, loopback port 8895 behind `tailscale serve`, image pinned by digest in `.env`. Warbler's
watcher handles updates. Backup remains open (E2): in-app nightly `VACUUM INTO` → R2, plus a
documented, actually-run restore drill.

---

## C. Enrichment pipeline

### C1. ~~Which model, and what exactly does it return?~~ — **settled: Jev + DeepSeek, two calls**
Different model types for different jobs:

- **Classification:** Jev (`typesafe/jev-1.14`) through Nova's `decide` — a `choice` question over
  the kind taxonomy, plus optional `noul` checks. Typed answers with probabilities; nothing to
  parse.
- **Tags, summary, title:** `deepseek/deepseek-v4.1-flash-1017` through Nova's
  `provider/openrouter` — 3–6 tags, a two-sentence summary, a cleaned title, strict JSON schema,
  prompt version stored for re-enrichment.

The split is deliberate: a chat model should not be asked to pick from a taxonomy and have its
prose parsed, and a decision model should not be asked to write a summary.

### C2. ~~What gets archived, and who renders it?~~ — **settled**
Every save gets a single-file **HTML** archive and a **screenshot** (the card image); **PDF is on
demand**. The Worker renders and writes to R2; if the Worker is down the save is still accepted and
the archive job retries — it never blocks the save.

### C3. ~~Embeddings: Workers AI or OpenRouter?~~ — **settled: OpenRouter + Nova `embed`**
- **Model:** `perplexity/pplx-embed-v1-0.6b` — **1024 dimensions**, 32K context, **$0.004/M
  tokens**. The cheap default; `pplx-embed-v1-4b` (2560 dims, $0.03/M) is the quality step-up if
  retrieval disappoints.
- **Normalization:** Perplexity vectors are **unnormalized** — compare with cosine, never inner
  product.
- **Where:** Nova's `embed` primitive (issue
  [#49](https://github.com/AutumnsGrove/nova/issues/49)); Harbor is its first consumer and
  added a [requirements comment](https://github.com/AutumnsGrove/nova/issues/49#issuecomment-6595253006).
  This is a hard dependency for semantic search, not for steps 0–7.
- **Storage:** 1024 × 4 bytes ≈ 4 KB per item; 10k items ≈ 40 MB in RAM. Store `model`, `dims`,
  and `content_hash` per row so a model swap is detectable and only changed rows re-embed.

### C4. ~~Failed jobs: what does the user see?~~ — **settled**
Retry **3× with backoff over ~an hour**, then park as `failed` with the error stored. A dismissible
"needs attention" strip in the UI, with one-tap retry. No silent failures.

### C5. ~~Poll cadence and pending visibility~~ — **settled**
30s poll loop with jitter; opening the UI triggers an immediate poll; pending cards render from the
poll response. A failed poll is not an error state — the loop backs off and retries.

---

## D. Front doors

### D1. ~~Is floccus viable?~~ — **dropped**
It targets WebDAV/Nextcloud Bookmarks/Google Drive/local files, not arbitrary JSON. The chosen
front doors are in-app, Telegram, iOS Shortcut / PWA share, and the quick-save page — no WebDAV
surface to maintain.

### D2. ~~Telegram bot: new bot or extend Mira?~~ — **settled: dedicated bot**
Own token, Worker webhook, isolated from Mira. Accepts links, text, photos, documents, and albums
(grouped by `media_group_id`). Reuses the same intake path as every other door.

### D3. ~~Is the iOS Shortcut worth it if the PWA share target exists?~~ — **settled: both**
Publish a Shortcut recipe and a PWA manifest `share_target`; both hit the same Worker endpoint.
Remaining detail (H4): what the Shortcut sends for a page with no metadata beyond the URL.

### D4. ~~Email-to-save in v1?~~ — **dropped**
Not one of the chosen doors. It can be added later as another Worker route writing to the same
`jobs` table without touching the rest of the system.

---

## E. Security & operations

### E1. ~~Auth model for the write endpoint?~~ — **settled: per-source tokens**
One bearer token per source (`telegram`, `shortcut`, `quick`), stored **hashed** in D1, revocable
individually. The token also identifies the source, so the item records which door it came from.

### E2. ~~Backup destination and retention.~~ — **settled**
Nightly `VACUUM INTO` → zstd → **R2 with 30 daily snapshots**, plus an `rsync` copy to the
**MacBook Air** (`100.94.193.102`, this machine) when it's online — not the mini. This is
Vaultwarden's existing backup pattern, reused. The media mirror is continuous and retrying, not
nightly. Restore drill documented and actually run once; restoring the DB without the media is not
a restore.

### E3. ~~Export format and commitment.~~ — **settled**
One command writes **JSON + Markdown + Netscape HTML**, plus a `media/` folder and `manifest.json`
so the export is restorable and readable without the app. Tested in CI with a fixture from day one.
Never a roach motel.

### E4. ~~How does the browser reach archived files in R2?~~ — **settled: presigned URLs**
Private bucket. The potato asks the Worker for a short-lived signed URL (~5 minutes) and the
browser fetches directly from R2; archive bytes never pass through the potato.

### E5. ~~Public sharing — confirm it's out.~~ — **settled: skip entirely**
No public read path exists. The Worker stays write-only. Revisit only if sharing becomes a real
need, with a narrow Worker-rendered snapshot rather than exposing the UI.

### E6. ~~Cost/usage guardrails~~ — **settled**
5 MB page cap, 8k model input tokens, 25 MB per uploaded blob, 20 MB per archive, 10 saves/min/IP.
When a cap or free tier is hit, the queue **pauses and reports** rather than failing permanently.

---

### E7. ~~R2 upload abuse and orphan cleanup~~ — **settled**
25 MB per-object cap, content-type allowlist (images/PDF/text), per-source token and per-IP rate
limits, `incoming/` prefix with a 7-day orphan sweep, and a storage-threshold alert. Per-source
tokens make abuse attributable and revocable.

---

## F. Polaris integration — **parked (not v1)**

The thread's `search_bookmarks()` tool was the original motivation, but Polaris does not support
MCP servers today and this is explicitly not a v1 concern. No work is planned here: no MCP
endpoint, no read-only SQLite mount, no schema coupling.

If it returns: a small read-only surface owned by Harbor (HTTP or MCP over the tailnet), not
Polaris opening `harbor.db` directly.

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
5. **Nova `embed` (#49)** — confirm `pplx-embed-v1-0.6b` really returns 1024 dimensions and that
   vectors arrive unnormalized, and that the `embed` surface lands before Harbor's
   semantic-search step.
6. **PDF text extraction in Go** — pick and prove a pure-Go library on a real text PDF and a real
   scan; measure extraction quality and RSS. The scan-detection threshold depends on this.
7. **Port 8895** — confirm it is free on the potato (`ss -ltn`) before installing.
8. **Video transcript source** — prove caption fetching from the potato's residential IP for a few
   real videos, including a no-caption case and a non-English track. Decide whether a bundled
   yt-dlp is needed or the caption endpoint alone is enough.

---

## H. Opened by the v1 scope answers

### H1. ~~Image pipeline details~~ — **settled**
- **Model:** `deepseek/deepseek-v4.1-flash-1017`, reused from text enrichment because it accepts
  image input. One call returns `{kind, tags, ocr_text, description, colors}`.
- **Tradeoff accepted:** a recent OCR benchmark put DeepSeek V4.1 Flash at ~80% versus Ling 3.0
  Flash VL at 90–93%. If screenshot/receipt OCR turns out too weak in practice, swap in
  `inclusionai/ling-3.0-flash-vl` — same OpenRouter key, same prompt shape.
- **Caps:** original ≤ 25 MB; thumbnail max 1600px, generated on the potato with Go's `image`
  package (not the Worker). Dedupe by SHA-256 before any model call.
- **Embed:** OCR text + description, same `pplx-embed-v1-0.6b` model as text.

### H2. ~~Highlight anchoring~~ — **settled: quote + prefix/suffix, versioned text**
Store `anchor_json = {quote, prefix, suffix, ordinal, text_hash}` where `text_hash` identifies the
extraction version the highlight was made against. On load, re-anchor the quote against the
current extraction.

- Exact match wins; a unique prefix/suffix match is accepted; an ambiguous or missing match marks
  the highlight **detached** and shows it separately ("couldn't re-anchor") rather than guessing.
- Never store DOM offsets or character offsets as the primary anchor — both drift silently after
  re-extraction.

### H3. ~~Smart Space rule vocabulary~~ — **settled: rules + AI suggestions**
- **Rules:** `{tags_all, tags_any, kinds, sources, after, before, text, similar_to_item}`.
- `auto` spaces evaluate live from their rule; `saved` spaces are explicit queries; `space_items`
  is only for manual pinning.
- **AI may suggest** a Space from a pattern ("you keep saving ARM SBCs — make a Space?"), but the
  user names it and chooses to keep it. The model never silently creates or renames a Space.

### H4. ~~What the in-app composer can save~~ — **settled**
URL + note + up to **4 images** per save, one `items` row. A quote is a note with
`kind=quote`. No rich-text editor, no tag picker at save time (edit tags after).

### H5. ~~Telegram media handling~~ — **settled**
Photos, documents, text, and albums (grouped by `media_group_id`). With no caption, the title falls
back to the AI description, then the filename.

---

---

## I. Adoption & lifecycle

### I1. ~~Import the existing library?~~ — **settled: Raindrop CSV + Netscape HTML**
Both importers in v1. CSV preserves title, note, excerpt, URL, tags, created date, cover, and
highlights; collections become **manual Spaces**. HTML folders become manual Spaces. Import is
idempotent by canonical URL and merges like a re-save. Available in Settings and as a CLI for big
files.

### I2. ~~Read state / reading queue?~~ — **settled: no read state**
A library first. **Read later** is an optional hand-curated **manual Space**, not an unread/read
boolean and not a queue. A real queue (progress, reminders, daily picks) is a later version if
it's ever wanted.

### I3. ~~Re-saving a duplicate URL?~~ — **settled: merge + refresh**
One canonical item per URL. Append the new note, union tags, keep Read later membership, refresh
metadata/archive/screenshot, bump `updated_at`. Canonicalisation drops fragments and tracking
parameters. A manual Refresh does the same without adding a note.

### I4. ~~Deleting an item?~~ — **settled: Recently deleted, 30 days**
Soft delete with an undo window, then purge the DB row (cascading media/highlights/tags), local
files, and R2 keys together. The Worker owns a token-gated purge route so the potato doesn't hold
R2 credentials for ordinary deletes.

### I5. ~~How editable is an item?~~ — **settled: Markdown, edit everything**
Title, note, tags, kind, and Read later are all editable. Notes render Markdown with a preview
toggle (sanitised server-side). The library is yours; the model's choice of kind is a default, not
a verdict.

---

## J. Shape of the library

### J1. ~~Kind taxonomy~~ — **settled: 12 kinds**
article, product, book, recipe, video, tweet, note, image, paper, podcast, place, movie.
`article` is the catch-all for written pages and unclassifiable links (including repos); the model
is never allowed to return `unknown`. Quote folds into note.

### J2. ~~Search behavior~~ — **settled: instant + hybrid + chips**
Results update as you type (~200 ms debounce, 2-character minimum). FTS5 and vector results are
merged with reciprocal rank fusion. Filter chips for kind, tag, source, and date. No separate
search page.

### J3. ~~Bulk actions~~ — **settled**
Multi-select with a bulk bar: **Delete, Tag, Add to Space, Refresh**. Long-press on mobile. Built
for cleaning up the Raindrop import.

### J4. ~~PDFs and documents~~ — **settled**
Local text-layer extraction for FTS5 and embeddings. If the text layer is empty or tiny compared
to the page count, OCR the first ~10 pages with `deepseek/deepseek-v4.1-flash-1017`, capped per
document. PDF archives stay on demand.

### J5. ~~Tag management~~ — **settled: full manager + AI normalization**
- On write: trim, collapse whitespace, lowercase, and **reuse an existing canonical tag** when one
  fits (the enrichment prompt carries the current vocabulary).
- Global tag manager: rename, merge, delete. A merge reassigns items and leaves the old spelling
  in `tag_aliases`, so a re-import or a later AI tag maps back to the canonical tag.

---

## K. Look, durability, and the wider web

### K1. ~~Visual direction~~ — **settled: dark-only editorial, homelab-adjacent**
Dark mode only. Calm editorial spacing, image-first masonry, a night-sky/amber hint from the
homelab family but its own identity. Mockups are a separate pass with Claude; this is direction,
not a spec.

### K2. ~~Backup encryption~~ — **settled: none extra**
Private R2 bucket + R2 server-side encryption. Accepted risk: anyone with bucket credentials can
read the library. Coffer encrypts because it holds financial data; this is a personal library, not
that.

### K3. ~~PWA scope~~ — **settled: installable + share target**
Manifest, icons, full-screen, Android `share_target`. **No service worker, no offline cache.**
The tailnet is assumed up.

### K4. ~~Video transcripts~~ — **settled: in v1, best-effort**
Fetch captions on the potato (not the Worker — residential egress behaves better), store in
`transcript_text`, feed FTS5 + embeddings. No captions → `transcript_state='unavailable'`, item
still succeeds. Podcasts use a published transcript if one exists; no audio transcription in v1.
Transcript source is a spike.

### K5. ~~Link rot~~ — **settled: scheduled re-archive + checker**
Daily staggered slice of items older than 30 days (capped ~50/day): refresh archive/screenshot,
set `link_status` to `ok|dead|error`, keep the last archive on death, and skip writes when the
archive `content_hash` is unchanged.

---

## L. Reading, first run, and self-knowledge

### L1. ~~Highlight scope~~ — **settled: articles only**
Reader view of extracted article text; select-to-highlight with an annotation. PDFs and archived
pages are view-only in v1. PDF highlighting is a later addition with its own viewer.

### L2. ~~First run~~ — **settled: import or first link**
A two-path empty state: **Import Raindrop** or **Paste a first link**. No multi-step wizard.

### L3. ~~Archive viewing~~ — **settled: reader primary, sandboxed viewer**
Reader text is the default. The archived HTML opens in a sandboxed viewer (scripts disabled, no
forms, isolated R2 origin) and is downloadable. The app never renders archived HTML unsandboxed.

### L4. ~~Observability~~ — **settled: logs + Status page**
Structured JSON logs to stdout, plus a Settings **Status** page: last poll, queue depth, last
backup, mirror lag, disk usage, last link check. No telemetry, no external logging.
