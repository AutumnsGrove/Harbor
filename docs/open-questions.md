# Open questions

The agenda for the next refinement pass. Nothing here blocks starting step 1 (the Worker's
`POST /save`), but most of it blocks step 2 onward.

Format: **question → why it matters → current lean.** Quoted answers get folded into
[design.md](design.md) and checked off here.

---

## A. Product shape

### A1. What is the minimum item type set for v1?
Links only? Links + freeform notes? Links + notes + images?

- **Why:** the `items` schema, the parser, and the UI all fork on this. Images imply uploads,
  storage, and vision models — a different cost profile than URLs.
- **Lean:** links + notes in v1 (a note is a row with no URL). Images are v1.5 via the Telegram
  bot, reusing the same row with a blob in R2.

### A2. What does "it organizes itself" mean, concretely?
A fixed `kind` taxonomy + AI tags? Or auto-assembled "spaces" (mymind's Smart Spaces) as a
first-class feature?

- **Why:** tags are a weekend; spaces are a query engine plus a UI concept. Choosing spaces early
  changes the schema (saved queries, membership) and the nav.
- **Lean:** fixed kind + AI tags in v1; tags are the only organizing axis. "Spaces" = saved
  searches later, once there's enough library to want them.

### A3. Do you actually want to read articles in this thing?
Reader view, highlights, annotations, PDF/EPUB reading — or is this purely "save, find, open in
the original tab"?

- **Why:** reader mode is cheap (Readability/trafilatura), but highlights + annotations are a
  second product surface (selection UI, anchoring, sync).
- **Lean:** clean reader view in v1 (you already extract the text); highlights in v1.5.

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
  `GET /jobs` returns queued rows, `POST /jobs/:id/{done,failed}` closes them.

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

### D1. Is floccus actually viable, and how?
floccus syncs browser bookmarks to **WebDAV, Nextcloud Bookmarks, Google Drive, or a local file**
— it does *not* POST arbitrary JSON to an HTTP endpoint. So "floccus → your API" needs either a
tiny WebDAV surface (on the Worker or potato) or a different bridge.

- **Why:** this was the "sneaky-best" save path in the thread, and the feasibility detail matters.
- **Lean:** confirm exact floccus target support; if generic POST isn't possible, expose a minimal
  WebDAV `PUT` on the potato (tailnet) or use a browser-side bookmark sync to the Worker later.
  **Needs a verification spike before committing to it.**

### D2. Telegram bot: new bot or extend Mira?
Where does the webhook live (Worker), and does it accept photos/files or just links/text?

- **Why:** the bot is the best mobile gesture, and a Worker webhook is ~50 lines. But Mira may
  already own that bot token.
- **Lean:** a dedicated bot + Worker webhook; accept text/URL/photo/forwarded messages; reuse the
  same `POST /save` path internally.

### D3. Is the iOS Shortcut worth it if the PWA share target exists?
- **Why:** Shortcuts work in every app on iOS; a PWA share target covers Android more cleanly.
  Both hit the same Worker endpoint.
- **Lean:** yes, publish a Shortcut recipe in the README; it's ~10 minutes once `/save` exists.

### D4. Email-to-save in v1?
Cloudflare Email Routing → Worker.

- **Why:** newsletters and receipts are exactly the things you'd never bother saving otherwise;
  but it's more Worker surface and another failure path.
- **Lean:** v1.5, after the core loop is trusted.

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

- **Why:** the potato's SD card is the single point of failure for the only irreplaceable data.
- **Lean:** nightly `VACUUM INTO` → R2 (30 days) **and** a copy to `mini` if it's always-on.
  Restore drill documented and actually run once.

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

### E5. Public sharing — confirm it's out.
- **Why:** the tailnet-only UI makes it impossible without a second read path; it's also the main
  thing that would force auth/multi-user.
- **Lean:** skip entirely. Revisit with a Worker-rendered read-only snapshot only if you ever
  actually need it.

### E6. Cost/usage guardrails.
Max page bytes, max LLM tokens, max archive size, rate limit on `/save`, and what happens when a
free tier is exceeded?

- **Why:** free tiers are generous, but a pathological page or a leaked token shouldn't generate a
  bill or an OOM.
- **Lean:** 5 MB page cap, 8k input tokens, 20 MB archive cap, 10 saves/min/IP; queue pauses
  instead of erroring.

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

1. **floccus target support** — can it POST to a custom endpoint, or only WebDAV/Nextcloud?
2. **Worker → D1 queue claim semantics** — confirm the atomic update pattern works with D1's
   consistency model, and what happens on duplicate delivery.
3. **Brute-force vector cache** — measure RSS and search latency on the potato at a realistic
   library size (10k vectors × 768 dims), and confirm the startup load time is acceptable.
4. **Browser Rendering from a Worker on the free plan** — confirm browser hours accounting and
   whether screenshot/PDF/HTML can be captured in one session.
5. **Polaris read path** — read-only bind mount of a WAL-mode DB from another container: safe, or
   does it need the HTTP API?
