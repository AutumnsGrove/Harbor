# Spikes — the twelve things to verify before building on them

A spike is a **timeboxed experiment with a written verdict**, not a prototype. Each one is ≤2 hours,
lives in a throwaway branch or a scratch directory (never `main`), and produces a paragraph plus a
number in this file. No spike leaves running code in the repo.

Rule: **if a spike fails, the plan changes — not the timeline.** A failed spike is a good outcome at
this stage; it is much cheaper than discovering it in phase 6.

These are the twelve from [open-questions.md §G](../open-questions.md#g-things-to-verify-with-a-spike-not-decisions)
(S1–S8 from the research thread, S9–S12 added by the 2026-11-04 plan audit), now with the phase each
one gates and the pass condition that has to be met.

| ID | Spike | Gates | Timebox |
|---|---|---|---|
| S1 | Worker streaming upload | phase 1 exit | 90 min |
| S2 | D1 claim semantics | phase 1 exit | 60 min |
| S3 | Brute-force vector cache | phase 10 exit | 90 min |
| S4 | Browser Rendering on the free plan | phase 11 exit | 2 h |
| S5 | Nova `embed` live call | phase 3 smoke / phase 10 exit | 45 min |
| S6 | PDF text extraction in Go | phase 6 exit | 2 h |
| S7 | Port 8895 free | phase 0 start | 5 min |
| S8 | Video transcript source | phase 7 exit | 90 min |
| S9 | Real Raindrop export sample | phase 5 start | 30 min |
| S10 | Tailscale serve headers + Docker peer address | phase 0 start | 45 min |
| S11 | Nova as a dependency in CI/Docker | phase 0 exit | 45 min |
| S12 | Image decode memory + EXIF | phase 6 exit | 90 min |

S7, S9, S10 and S11 are cheap and can be done now (S9 is ten minutes in Raindrop's export screen
plus sanitising). S1/S2 are prerequisites for writing the Worker; do them first.
S3/S4/S6/S8 are cheap to defer until their phase, but each can invalidate a design assumption, so
they must be answered *before* the phase's code is finished, not after.

---

## S7 — Port 8895 is free

**Question:** is 8895 actually unused on the potato, given 8893 Cairn and 8894 Coffer are reserved?

```bash
ssh potato-remote 'ss -ltn | grep -E ":(8893|8894|8895)\b" || echo "8895 free"'
```

**Pass:** no listener on 8895, and `docker ps` shows no container publishing it.
**Fail:** pick the next free port, update design.md §3, [meridian.md](../meridian.md), and
`.env.example`. One commit.

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
non-ASCII). Commit the sample as `server/internal/import/testdata/raindrop/real-shape.csv` (and the
HTML twin) in the phase 5 branch.

**Pass:** the column list is written down exactly (is there an `id`? a `favorite`? how are nested
collections written? how are several highlights packed into one cell, with what delimiter, and do
they carry a note or colour?); the design's CSV mapping table is corrected to match; unknown columns
are listed.
**Fail / surprise:** any field that cannot be mapped is a decision in design.md §5, not an importer
guess.

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

---

## S11 — Nova as a dependency

**Question:** can CI and the Docker build fetch Nova, and which version has `embed`?

**Method:** in a scratch module, `go get github.com/AutumnsGrove/nova@<candidate>`; then do it again
from a clean GitHub Actions job and a clean `docker build` with no host credentials.

**Pass:** a tag or commit is chosen and pinned; if the repo is private, `GOPRIVATE` plus a read-only
deploy key or token as a build secret works in both CI and the Dockerfile, and the secret does not
land in an image layer.
**Fail:** vendor Nova, or publish it, before phase 0's CI can be green.

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
