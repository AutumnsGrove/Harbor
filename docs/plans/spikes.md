# Spikes — the twelve things to verify before building on them

A spike is a **timeboxed experiment with a written verdict**, not a prototype. Each one is ≤2 hours,
lives in a throwaway branch or a scratch directory (never `main`), and produces a paragraph plus a
number in this file. No spike leaves running code in the repo.

Rule: **if a spike fails, the plan changes — not the timeline.** A failed spike is a good outcome at
this stage; it is much cheaper than discovering it in phase 6.

These are the twelve from [open-questions.md §G](../open-questions.md#g-things-to-verify-with-a-spike-not-decisions)
(S1–S8 from the research thread, S9–S12 added by the 2026-11-04 plan audit), now with the phase each
one gates and the pass condition that has to be met.

| ID | Spike | Gates | Timebox | Verdict |
|---|---|---|---|---|
| S1 | Worker streaming upload | phase 1 exit | 90 min | **pass** |
| S2 | D1 claim semantics | phase 1 exit | 60 min | **pass** |
| S3 | Brute-force vector cache | phase 10 exit | 90 min | pending |
| S4 | Browser Rendering on the free plan | phase 11 exit | 2 h | pending |
| S5 | Nova `embed` live call | phase 3 smoke / phase 10 exit | 45 min | **pass** |
| S6 | PDF text extraction in Go | phase 6 exit | 2 h | pending |
| S7 | Port 8895 free | phase 0 start | 5 min | **changed-plan → 8897** |
| S8 | Video transcript source | phase 7 exit | 90 min | pending |
| S9 | Real Raindrop export sample | phase 5 start | 30 min | **pass, plan corrected** |
| S10 | Tailscale serve headers + Docker peer address | phase 0 start | 45 min | **pass** |
| S11 | Nova as a dependency in CI/Docker | phase 0 exit | 45 min | **pass** |
| S12 | Image decode memory + EXIF | phase 6 exit | 90 min | pending |

S7, S9, S10 and S11 are cheap and can be done now (S9 is ten minutes in Raindrop's export screen
plus sanitising) — S7, S9, S10 and S11 are all answered now. S1/S2 are prerequisites for writing
the Worker; they were run on 2026-11-19 and both passed, so phase 1 is unblocked.
S3/S4/S6/S8 are cheap to defer until their phase, but each can invalidate a design assumption, so
they must be answered *before* the phase's code is finished, not after.

S5, S7, S10 and S11 were run on 2026-11-18. S7 changed the port; S5 and S10 passed in full; S11
passed locally, and its GitHub Actions half was confirmed on 2026-11-19 by the first green `main`
run and an anonymous pull on the potato. S1 and S2 were run on 2026-11-19 and both passed (S1 with a
design correction about chunked bodies). S9 was run on 2026-11-22 against the real export and passed
with one plan correction (highlights are prose, not structured rows; favourites are imported). The
remaining spikes are still open.

---

## S7 — Port 8895 is free

**Question:** is 8895 actually unused on the potato, given 8893 Sepia, 8894 Coffer and 8896 Outpost are already assigned?

```bash
ssh potato-remote 'ss -ltn | grep -E ":(8893|8894|8895)\b" || echo "8895 free"'
```

**Pass:** no listener on 8895, and `docker ps` shows no container publishing it.
**Fail:** pick the next free port, update design.md §3, [meridian.md](../meridian.md), and
`.env.example`. One commit.

**Verdict (2026-11-18): changed-plan — 8895 is taken by Pixelsort.**
**What happened:** `ss -ltn` on the potato shows `127.0.0.1:8895` and the tailnet address on 8895 in
use; `docker ps` shows `pixelsort-pixelsort-1 127.0.0.1:8895->8895/tcp`, and the homelab port map
lists 8895 as Pixelsort. `tailscale serve` already proxies `:8895` to it. Port **8897** is free on
both loopback and the tailnet, with no Docker container publishing it.
**Consequence:** Harbor now uses **8897** everywhere (design §3, README, meridian.md, plans,
mockup). On deploy day the homelab port map adds "8897 Harbor" (8895 stays Pixelsort) and the
Meridian snippet points at 8897. Do not reuse 8895.

---

## S1 — Worker streams a 25 MB upload to R2

**Question:** does `env.BUCKET.put(key, request.body)` stream a 25 MB body on the free plan without
buffering it in the isolate or blowing the CPU/wall-time budget?

**Method:** throwaway worker + `wrangler dev --remote`, upload a 25 MB file three times, then a
26 MB file, and read `wrangler tail` for duration and memory. Also try a slow client (a `curl
--limit-rate 200k`) to see whether the stream holds or the request times out.

**Pass:** 25 MB lands in R2 in one request; peak memory stays flat as size doubles (proxy: no
`RangeError` / isolate reset and no memory warning); 26 MB is rejected by the size cap *before* the
body is consumed (check `Content-Length` and be honest that a chunked body can only be cut off
mid-stream); the request completes well inside free-tier limits.

**Fail / partial:** if buffering is real, the cap drops to a size that streams reliably (record it)
or the upload path becomes "presign, upload directly from the client, Worker records the key". Both
are contained changes — but the second touches the token model and must be decided in design.md
before phase 1 is called done.

**Also record:** the honest answer to "can the Worker know the size before writing?" (it cannot for
a chunked body), because it decides whether the 25 MB cap is a guarantee or a best effort.

**Also test (added 2026-11-04):**
- **Unknown length.** Does `BUCKET.put` accept `request.body` from a chunked request, or does R2
  require a known length (`Content-Length` / `FixedLengthStream`)? If it requires one, `/upload`
  answers 411 without a `Content-Length` and the 25 MB cap becomes a guarantee, not a best effort —
  record which, because design §5 currently states "`Content-Length` is required" on this
  expectation.
- **Telegram files.** Bot API `getFile` downloads cap at 20 MB. Stream a ~19 MB and a ~21 MB file
  from Telegram through the Worker to R2 and record the behaviour; the rejection reply must name the
  real limit.
- **Validator in Workers.** Run the chosen JSON-Schema validator inside `wrangler dev`. Ajv's code
  generation uses `new Function`, which Workers forbid; confirm `@cfworker/json-schema` (or
  precompiled validators) works against `contracts/*.schema.json`.

**Verdict (2026-11-19): pass, with one design correction and one caveat.**
**What happened:** a throwaway worker (`harbor-spike-s1`, R2 bucket of the same name) deployed to
`workers.dev`, exercised with `curl`, a raw `http.client` chunked writer and `wrangler tail`.

| Upload | Result | Worker `wallTime` / `cpuTime` |
|---|---|---|
| 25 MB declared, ×3 | 2 landed byte-exact; 1 failed with R2 `10043` and wrote **nothing** | 1.5–2.3 s / 1 ms |
| 26 MB, 50 MB declared | 413, `ms: 0` — rejected before any `put` | 0 ms / 0 ms |
| 19 MB, 21 MB (Telegram sizes) | 200, byte-exact | 1.6 s / 1 ms |
| 5 MB at `--limit-rate 200k` (27 s) | 200 | 1.1 s / 1 ms |
| chunked (`TE: chunked`, no length) 1 MB / 26 MB | 200 / 413, same as declared | 0.8 s / 0 ms |

**The correction:** a chunked body **does** reach the Worker with a `content-length`. A raw
`Transfer-Encoding: chunked` writer (not curl — curl can buffer) arrives at the Worker with
`content-length: 27262976` and the 26 MB case is a 413 with `ms: 0`. Cloudflare's edge de-chunks and
buffers the request before the isolate runs, so **the 25 MB cap is a guarantee, not best effort**, and
`/upload` does not need to return 411 for a missing `Content-Length` — it needs to treat a missing one
as "no body" rather than "unknown size". No `RangeError`, no isolate reset, no memory warning in ~20
events, and CPU stays at 0–3 ms (the free plan allows 10 ms/invocation). Slow clients are fine: the
wall time is the client's upload, not the Worker's.

**The caveat:** the `10043` on one of three 25 MB puts is real (R2 returned an error; nothing was
written, so it is a clean failure, not a partial object). Every upload path needs a retry, and the
potato must still verify size + sha256 when it fetches a blob.

**Also found:** a client that lies about `Content-Length` cannot bypass the cap — the runtime reads
exactly the declared number of bytes and discards the rest. But it *stores what was declared*, so a
mislabelled upload becomes a silently **truncated** blob (a claimed 26214400 bytes out of a real
26 MB arrived as exactly 26214400 bytes). The consumer-side hash catches it; do not assume the size
on the blob row is the file's size.

**Validator:** `@cfworker/json-schema` works in workerd and returns structured errors
(`instanceLocation`, `keyword`, `keywordLocation`, `error`). Ajv's default, compiled inside the
worker, throws `EvalError: Code generation from strings disallowed for this context` — the plan's
claim, confirmed. Use `@cfworker/json-schema`, or Ajv's standalone precompiled validators; not
`new Ajv().compile()`.

**Consequence for phase 1:** the streaming design stands as written (no presign/direct-upload
fallback needed), `Content-Length` handling in plan 01 changes from "411 otherwise" to "cap it when
present, 0-length body otherwise", and the plan gains a retry plus a truncation check.

---

## S2 — D1 claim-by-update

**Question:** does an atomic claim work with D1's consistency model, and what happens on duplicate
delivery?

**Method:**

```sql
UPDATE jobs SET status='claimed', claimed_at=strftime('%s','now'), attempt=attempt+1
WHERE id = (SELECT id FROM jobs WHERE status='queued'
            ORDER BY created_at LIMIT 1)
RETURNING *;
```

Run it concurrently from two clients (loop 200 times, two `Promise.all` workers, `wrangler dev`),
and check for double-claims. Then test the crash case: claim a job and never report `done`.

**Pass:** zero double-claims across 200 attempts; `RETURNING` gives the whole row; a claimed job is
re-claimable after a lease (plan: 10 min) and `attempt` increments monotonically; `done` on an
already-done job is a no-op, not an error.

Also run the **batch form** the potato uses (`WHERE id IN (SELECT id … LIMIT 5)`), and the hold
rule for Telegram albums (`group_key IS NULL OR updated_at < now - 10`), 200 times each from two
clients: zero double-claims, and a held album is never claimed early.

**Fail:** fall back to a single-statement `INSERT … ON CONFLICT` ledger or accept at-least-once with
the potato's merge semantics as the dedupe (it already has one, by canonical URL and sha256). Record
which, plus the exact SQL, in the phase 1 plan.

**Note:** if the lease does not exist in D1 as a concept, it is our column and our sweep. Decide the
sweep's home — the Worker's cron, not the potato — so a dead potato doesn't strand jobs forever.

**Verdict (2026-11-19): pass.**
**What happened:** `harbor-spike-s2` (D1 `harbor-spike-s2`, the phase-1 schema) with `UPDATE … WHERE
id IN (SELECT …) RETURNING *`, claimed through HTTP from node clients:

| Shape | Claims | Unique | Overlapping claims | Notes |
|---|---|---|---|---|
| 2 clients × 200 sequential, `limit=1`, 200 jobs | 200 | 200 | **0** | 63 s, ~80–160 ms/claim |
| 4 clients × 40, `limit=5`, 200 jobs | 200 | 200 | **0** | 9 s |
| 40 simultaneous claims, `limit=1`, 200 jobs | 40 | 40 | **0** | one shot |
| 12 jobs, `limit=5` ×3 | 5 / 5 / 2 | 12 | 0 | no overlap between batches |

`RETURNING *` gives the whole row, including `payload_json`; the batch form is atomic; the two-client
shape from the plan produced zero double-claims. `done` twice answers `changes: 1` then `changes: 0`
with the same `done_at` — idempotent, not an error. A claim with `lease=-1`, then the reclaim sweep,
then a second claim gives `attempts` 1 → 2 and a fresh lease; a claim inside a live lease is refused.
An album job touched 3 s ago is not claimable, one touched 20 s ago and an ordinary job are, and a
claim never moves `touched_at` (it writes `updated_at`).

**The finding that matters:** the claim is one atomic statement, and anything you write *after* it in
the same request can fail while the claim has already committed. The first version of the audit table
had `PRIMARY KEY (job_id)`; re-claiming an expired job after the sweep then threw on the audit insert
and the client got an empty 500 for a claim that had succeeded. Keep the claim single-statement and
treat its response as the truth; any bookkeeping write must be `INSERT OR IGNORE`-shaped or dropped.
A double-claim is best detected as *two claim rows whose lease windows overlap*, not as "two rows for
one job" — the latter is the lease working as designed.

**Consequence for phase 1:** the SQL in plan 01 is correct as written, including the batch form and
the `touched_at` hold; the sweep belongs in the Worker's cron; no `INSERT … ON CONFLICT` ledger or
at-least-once fallback is needed. Add the "claim first, everything else tolerant" rule to the plan.

---

## S3 — Brute-force vector cache at 10k items

**Question:** what does exact cosine over an in-memory float32 slice cost at a realistic library
size on the potato, in RSS and in latency?

**Method:** generate 10k random 1024-dim float32 vectors (40 MB), load into a slice, then time 200
queries (single-threaded, then 4 goroutines) and read RSS before/after. Repeat with 30k to find the
knee. Measure the startup load from SQLite BLOBs too.

**Pass:** 10k vectors load in <2 s, sit under ~60 MB RSS, and a top-50 query returns in <50 ms
single-threaded — leaving room for FTS5 and fusion inside the 200 ms search budget.
**Fail:** at 10k the numbers are worse than that, then either (a) move the cache to `[]float32` with
normalisation on write and skip the division per query, (b) quantise to int8 for the first pass and
rescore the top 200, or (c) revisit `sqlite-vec` and its cgo cost. Record the choice in design.md §8.

**Also record:** whether 30k is still fine. That is the next checkpoint after Polaris's ~50k
revisit point, and it should be written down rather than guessed.

---

## S4 — Browser Rendering: HTML + screenshot (+ PDF) in one session

**Question:** can one Browser Rendering session produce the HTML archive, the screenshot used as a
card, and (on demand) a PDF, on the free plan, within the CPU/wall limits?

**Method:** a throwaway Worker route that opens three real pages — a static article, a JS-heavy SPA,
and a page with a consent wall — and attempts, in one session: `content()` for HTML, `screenshot()`
at 1200×630, then `pdf()`. Log browser-time, wall-time, and failures per stage.

**Pass:** HTML + screenshot succeed in one session for all three pages; PDF works either in the same
session or reliably in a second; a failed stage does not lose the other artifacts; total browser
time per save is comfortably inside the daily free-tier allowance for ~100 saves/month.
**Fail:** split into two sessions (cheap, loses a little time) or move screenshots to `screenshot()`
only and skip HTML for SPA pages, recording why. Either way: archive failure must remain a
non-blocking, retryable job (design.md §6).

**Also record:** the per-save free-tier budget arithmetic, in numbers, for the Settings/Status page
and for the cost guardrail. The free plan is 10 browser-minutes a day and 3 concurrent browsers; the
link checker renders only when its potato-side content hash changed, so budget for new saves plus a
small number of changed pages, not 50 renders a day.

---

## S5 — Nova `embed` returns what design.md says

**Question:** does one live call through Nova's `embed` primitive with the `index` role return a
**1024-dim, unnormalised** vector for `perplexity/pplx-embed-v1-0.6b`, and does it record usage/cost?

**Method:** a tiny Go program (throwaway, not in `server/`) that embeds two short texts and prints
`len(vec)`, the L2 norm, and the usage. Run it twice with the same text.

**Pass:** dims == 1024; norm ≠ 1.0 (proving unnormalised, so cosine is mandatory); deterministic for
identical input; usage/cost visible. Then confirm `embed.NewRoles` with the documented `Dims: 1024`
rejects a mismatched model instead of silently truncating.
**Fail:** anything else is a Nova issue, filed before phase 3 depends on it — and phase 10 is the
blocked one, not phase 0.

**Verdict (2026-11-18): pass.**
**What happened:** two live calls through `embed.NewRoles` with the `index` role and
`perplexity/pplx-embed-v1-0.6b` returned **1024 dims**, L2 norm **5.151501** (not 1, so the vectors
are unnormalised), and identical vectors for identical input. Usage was reported: 14 tokens,
`$0.000000056` per call. A deliberately wrong `Dims: 512` role was refused with
`embed: role "index": perplexity/pplx-embed-v1-0.6b returned 1024 dimensions, want 512`, not
silently truncated.
**Consequence:** design §6/§8 stands: normalise on write, compare with cosine, store `dims`.

---

## S6 — PDF text extraction in pure Go

**Question:** which pure-Go PDF library gives usable text on a real text PDF and cleanly identifies a
scan, with bounded memory?

**Method:** two real documents — a digital-text PDF (10–40 pages) and a scan of similar size — plus
one deliberately malformed file. Try `pdfcpu`'s extraction and one of `rsc.io/pdf` /
`ledongthuc/pdf` / `unipdf` (note: `unipdf` is not free for commercial use, irrelevant here but
recorded). Measure extracted characters, whitespace/line-break quality, page count, time, and peak
RSS. Compute the text-to-page ratio the scan heuristic would use.

**Pass:** text PDF extracts cleanly enough to read and to search (spot-check 10 phrases); the scan
yields near-zero text, which the threshold can detect; malformed input returns an error within a
timeout and does not allocate wildly; peak RSS under ~150 MB for a 40-page file.
**Fail:** if no library is good enough, the fallback is Worker-side rendering of the PDF pages and
OCR through the vision model with a hard page cap — more expensive, so it must be a deliberated
decision in design.md §6, not a silent substitution.

**Also record:** the chosen library, the scan threshold as a *config default* (e.g. "fewer than 50
chars/page over ≥3 pages"), and the per-document OCR page cap.

---

## S8 — Video transcript source from the potato's IP

**Question:** can the potato fetch captions for real YouTube videos reliably enough to be a v1
feature, including no-caption and non-English cases?

**Method:** from the potato (residential egress), for 10 real videos: the timedtext/caption endpoint
with a normal user agent, a preferred-language request (`en`, then any), and one auto-generated
track. Include one video with captions disabled and one non-English video with English auto-captions.
Then repeat from a Cloudflare Worker IP to confirm the *reason* the design puts this on the potato.

**Pass:** ≥8/10 videos return usable text; no-caption returns a clean "unavailable" signal; the
potato clearly outperforms the Worker IP; no dependency on a login.
**Fail:** if the endpoint is too fragile, options in order: (a) a bundled `yt-dlp` in the image
(+40 MB, adds an update surface), (b) transcript "unavailable" in v1 and the phase 7 scope shrinks to
podcasts-with-published-transcripts, (c) drop phase 7 to a later version. Record the choice in
design.md §6/§13 — a flaky transcript path must never be able to fail a save either way.

---

## S9 — A real Raindrop export

**Question:** what does Raindrop's CSV and HTML export actually contain, so the importer is written
against reality and not against the design doc's recollection?

**Method:** export the live library as CSV and as HTML. Sanitise to a ~50-row sample (replace real
URLs with `example.com/<n>`, keep structure, quoting, collection paths, multi-line notes, highlights,
non-ASCII). Commit the sample as `server/internal/importer/testdata/raindrop/real-shape.csv` (and the
HTML twin) in the phase 5 branch.

**Pass:** the column list is written down exactly (is there an `id`? a `favorite`? how are nested
collections written? how are several highlights packed into one cell, with what delimiter, and do
they carry a note or colour?); the design's CSV mapping table is corrected to match; unknown columns
are listed.
**Fail / surprise:** any field that cannot be mapped is a decision in design.md §5, not an importer
guess.

**Verdict (2026-11-22): pass, with one plan correction (highlights).**

Run against `~/Downloads/3cad2d1c-677d-4697-a100-69c64d3e72f4.csv` (the later of the two exports;
the earlier `0c682afb-…` file has the same columns, 1,426 rows and the same shapes). Both were left
where they are; nothing raw is in the repository.

**Columns — exactly the eleven the plan predicted, in this order, no unknowns:**

```
id,title,note,excerpt,url,folder,tags,created,cover,highlights,favorite
```

1,476 rows, 1,476 distinct `id`s and 1,476 distinct `url`s (nothing to deduplicate in this export).
UTF-8, **no BOM, no CRLF**, `\n` line endings.

| Field | What it actually is | Count in 1,476 rows |
|---|---|---|
| `id` | Raindrop's numeric id as a string | 1,476 |
| `title` | plain text, no newlines; commas and `"` appear | 1,476 (none empty) |
| `note` | Markdown-ish free text, embedded newlines, 4 end with a trailing newline | 376 |
| `excerpt` | page description; often truncated ending in `...`, often with a blank-line paragraph break | 1,310 (237 end in `...`, 182 contain `\n\n`) |
| `url` | full URL, query strings kept (567 have a query, 16 a fragment, 26 a `utm_`), 3 are `http://` | 1,476 |
| `folder` | **one flat collection name**, never a path — 24 distinct values, incl. `Read Later` and `Unsorted` | 1,476 |
| `tags` | one cell, `, `-separated, no newlines, no trailing comma; names may contain spaces, and case is inconsistent (`3D` and `3d` both exist) | 1,290 rows, 2,134 distinct tags |
| `created` | RFC 3339 with milliseconds and `Z`: `2026-03-08T21:44:42.849Z` | 1,476 (one format) |
| `cover` | an absolute image URL (`https` 1,287, `http` 38); never a local path | 1,325 |
| `highlights` | **free prose, not JSON** — see below | 98 cells, 268 blocks |
| `favorite` | the literal strings `true`/`false` | 6 `true` |

567 rows contain non-ASCII (curly quotes, em/en dashes, emoji in titles, accented and CJK text).

**Highlights: the plan was wrong.** The plan's table assumed `quote`, `note` and `colour` columns.
Reality is a single prose cell with no colour, no delimiter and no per-highlight annotation:

- Blocks are `Highlight:<quote text>` separated by a blank line, i.e. `\n\nHighlight:`.
- 268 blocks across 98 cells (1–19 per cell; median cell 351 bytes, longest 2,372).
- The quote text inside a block may itself contain blank lines and single newlines, so the **only**
safe split is a blank line *followed by* `Highlight:` — never a bare blank line. Splitting on `\n\n`
alone turns multi-paragraph quotes into fragments (`Be human.` becomes its own block).
- Some blocks are scraped page junk rather than prose (one cell ends `… \n  Subscribe`), and some are
very short (`Highlight:unusual`). There is no way to tell a quote from the user's own annotation, so
nothing is guessed: **one `highlights` row per block**, the quote is the block text minus the
`Highlight:` prefix, `note` stays empty, and `anchor_json` says `{"source":"imported","index":n}`.
Rejoining an item's rows in index order with `\n\nHighlight:` reproduces the original cell up to the
surrounding whitespace of each block, so the import is lossless where it matters: no passage is
dropped, merged or reordered.

**Favourites are now imported.** With the count known (6), Autumn asked for favourites to be a real
thing rather than a dry-run statistic: they land in a seeded manual Space named **Favourites**
(`spaces.slug = 'favourites'`, the same shape as Read later), created by the import phase's
migration. The item-level star toggle and the Space's UI arrive with phase 9's Space work.

**The HTML export was not needed to answer this.** Only CSVs were exported; phase 12a already writes
Netscape HTML and phase 5's round-trip test uses that writer, so the HTML importer is written
against the documented format plus that writer rather than a second Raindrop file.

**Fixture.** `server/internal/importer/testdata/raindrop/real-shape.csv` carries these shapes (15 rows:
multi-line notes, doubled quotes, a noisy URL with `utm_`/fragment, `http` cover, 1/2/4-block
highlight cells, a block with internal newlines, a 9-character block, scraped-junk text, unicode and
emoji, an empty title, a duplicate URL, slack in the tag cell). It is **hand-built to the observed
shapes, not a mechanical sanitisation of her export**: the repository is public, so her note and
highlight prose is not committed even though the spike suggested keeping it. All URLs are
`example.com`.

**Consequence:** design.md §5 and plans/05 now carry the corrected mapping (highlights, favourites,
flat folders). The parser must be header-driven and report unknown columns; a missing header is a
typed error, not a positional guess.

---

## S10 — Tailscale serve headers and the Docker peer address

**Question:** what does the identity lock actually see?

**Method:** on the potato, run a throwaway container listening on a published loopback port behind
`tailscale serve`. Log the headers and the peer address for (a) a tailnet browser request, (b) a
`curl` from the potato host, (c) a request with a forged `Tailscale-User-Login`.

**Pass:** (a) carries `Tailscale-User-Login` and the Host you expect; (b) carries no identity header;
(c) the forged header is overwritten or stripped by `tailscale serve` for tailnet traffic — if it is
*not*, the "absent header means host-local" rule for `/healthz` and `/api/status` needs a second
factor. Also record the peer address inside the container for (a) and (b) (expected: the Docker
bridge gateway for both, which is why loopback is not usable as a test).
**Fail:** if the header can be forged by a tailnet client, the lock must additionally verify the
caller with `tailscale whois` against the peer address, and the plan changes.

**Verdict (2026-11-18): pass.**
**What happened:** a throwaway container on `127.0.0.1:8897`, published through
`tailscale serve --https=8897`, was called four ways. A tailnet request carried a real
`Tailscale-User-Login`, `Tailscale-User-Name`, `X-Forwarded-For` (the Mac's tailnet address) and
`X-Forwarded-Proto: https`; the peer inside the container was **172.17.0.1** (the Docker bridge
gateway). A forged `Tailscale-User-Login` on the same tailnet request was **overwritten** with the
real login, not passed through. A `curl` from the potato host carried no identity header (and can
forge one, which is why host-local callers are trusted). The temporary serve mapping and container
were removed after the test.
**Consequence:** the header-based identity lock in design §3 is correct. Keep `/healthz` and
`/api/status` as the only header-absent exemptions; do not use the peer address as a loopback test.
Record `X-Forwarded-For` as diagnostic only, never as identity.

---

## S11 — Nova as a dependency

**Question:** can CI and the Docker build fetch Nova, and which version has `embed`?

**Method:** in a scratch module, `go get github.com/AutumnsGrove/nova@<candidate>`; then do it again
from a clean GitHub Actions job and a clean `docker build` with no host credentials.

**Pass:** a tag or commit is chosen and pinned; if the repo is private, `GOPRIVATE` plus a read-only
deploy key or token as a build secret works in both CI and the Dockerfile, and the secret does not
land in an image layer.
**Fail:** vendor Nova, or publish it, before phase 0's CI can be green.

**Verdict (2026-11-19): pass, including CI.**
**What happened:** `github.com/AutumnsGrove/nova` is public. A clean `go get` of commit `ef460da`
(the commit with `embed`, `embed/openrouter` and `embed.NewRoles`) succeeded from a scratch module
and from a clean `golang:1.27-alpine` Docker build with no credentials or `GOPRIVATE`. The module
resolved as `v0.5.1-0.20261116000603-ef460da11cfc`. The GitHub Actions half is now proven: every
run of the `check`, `fuzz`, `image` and `publish` jobs on `main` fetches modules in the Go stage
with no secret, and the published image pulls anonymously on the potato (no `~/.docker/config.json`
entry).
**Consequence:** pin Nova to that commit (or a later tag) in `go.mod`; no build secret is needed.
Re-check before phase 3 if the repository's visibility changes.

---

## S12 — Image decode memory and EXIF orientation

**Question:** what does decoding a phone photo cost on this box, and how is orientation handled?

**Method:** a throwaway Go program on the potato: decode a real 12 MP and a real 48 MP iPhone JPEG
with the standard library, print `runtime.MemStats` peak and wall time; scale to 1600 px. Try one
EXIF-orientation approach (a small parser or a vetted library) on photos taken in all four
orientations. Check which formats the standard library can decode (JPEG, PNG, GIF; WebP decode needs
`golang.org/x/image/webp` and there is no encoder).

**Pass:** the 48 MP case fits a budget you are willing to give one decode (a pixel cap of ~24 MP is a
likely outcome; pick it from the number); orientation is correct in all four cases; the thumbnail is
written as JPEG.
**Fail:** lower the cap, or shell out to nothing (there is no image tool in a distroless image) — the
fallback is to store oversized images undescribed with a badge, never to lose the file.

---

## Recording a result

Append to the table at the top and replace the spike's section with:

```
**Verdict (YYYY-MM-DD):** pass | fail | changed-plan
**What happened:** two or three sentences, with the numbers.
**Consequence:** what changed in design.md / which plan step changed.
```

The spike docs are deliberately kept after the fact: the *numbers* are what stop a future decision
from being re-litigated on vibes.
