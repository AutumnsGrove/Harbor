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

### C2. What gets archived, and who renders it?
Screenshot + PDF + single-file HTML on every save via Browser Rendering, or on demand? Keep an
HTML-only fallback (monolith) if the Worker is down?

- **Why:** archive size vs fidelity vs cost. Also determines what "open archive" means in the UI.
- **Lean:** HTML (single-file) always; screenshot for cards; PDF only on demand. Fall back to
  `monolith`-style local capture when the Worker isn't available.

### C3. ~~Embeddings: Workers AI or OpenRouter?~~ — **settled: OpenRouter + Nova `embed`**
- **Model:** `perplexity/pplx-embed-v1-0.6b` — **1024 dimensions**, 32K context, **$0.004/M
  tokens**. The cheap default; `pplx-embed-v1-4b` (2560 dims, $0.03/M) is the quality step-up if
  retrieval disappoints.
- **Normalization:** Perplexity vectors are **unnormalized** — compare with cosine, never inner
  product.
- **Where:** Nova's `embed` primitive (issue
  [#49](https://github.com/AutumnsGrove/nova/issues/49)); LocalDrop is its first consumer and
  added a [requirements comment](https://github.com/AutumnsGrove/nova/issues/49#issuecomment-6595253006).
  This is a hard dependency for semantic search, not for steps 0–7.
- **Storage:** 1024 × 4 bytes ≈ 4 KB per item; 10k items ≈ 40 MB in RAM. Store `model`, `dims`,
  and `content_hash` per row so a model swap is detectable and only changed rows re-embed.

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

### E1. ~~Auth model for the write endpoint?~~ — **settled: per-source tokens**
One bearer token per source (`telegram`, `shortcut`, `quick`), stored **hashed** in D1, revocable
individually. The token also identifies the source, so the item records which door it came from.

### E2. ~~Backup destination and retention.~~ — **settled**
Nightly `VACUUM INTO` → zstd → **R2 with 30 daily snapshots**, plus an `rsync` copy to the
**MacBook Air** (`100.94.193.102`, this machine) when it's online — not the mini. This is
Vaultwarden's existing backup pattern, reused. The media mirror is continuous and retrying, not
nightly. Restore drill documented and actually run once; restoring the DB without the media is not
a restore.

### E3. Export format and commitment.
JSON + Markdown + Netscape HTML, one CLI command? Guarantee it never becomes a roach motel.

- **Why:** data ownership was a primary motivation; mymind's one-way export is the cautionary
  tale.
- **Lean:** `localsave export --format {json,md,html}` from day one, tested in CI with a fixture.

### E4. ~~How does the browser reach archived files in R2?~~ — **settled: presigned URLs**
Private bucket. The potato asks the Worker for a short-lived signed URL (~5 minutes) and the
browser fetches directly from R2; archive bytes never pass through the potato.

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
- **Lean:** 25 MB per-object cap, content-type allowlist (images/PDF/text), per-source token rate
  limit and per-IP limit, `incoming/` prefix with a 7-day orphan sweep, and an alert at a storage
  threshold. Per-source tokens are settled (E1), so abuse is at least attributable and revocable.

---

## F. Polaris integration — **parked (not v1)**

The thread's `search_bookmarks()` tool was the original motivation, but Polaris does not support
MCP servers today and this is explicitly not a v1 concern. No work is planned here: no MCP
endpoint, no read-only SQLite mount, no schema coupling.

If it returns: a small read-only surface owned by LocalDrop (HTTP or MCP over the tailnet), not
Polaris opening `localdrop.db` directly.

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
   vectors arrive unnormalized, and that the `embed` surface lands before LocalDrop's
   semantic-search step.

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
