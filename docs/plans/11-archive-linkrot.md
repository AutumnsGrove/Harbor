# Phase 11 — Archive and link rot

**Status:** planned · **Design refs:** [§5](../design.md#5-save-path) (archives),
[§11](../design.md#11-operations) (link checking, archive history), [C2/K5/L3](../open-questions.md)
**Depends on:** phases 1, 3 · **Gate:**
[S4](spikes.md#s4--browser-rendering-html--screenshot--pdf-in-one-session) · **Est:** a weekend

## Goal

Links keep their contents, and stay honest when the web changes. Every save gets a single-file HTML
archive and a screenshot; PDF is on demand. A daily staggered slice of older items is re-fetched, and a
404 shows an amber "link dead" badge while keeping the last archive. Archived HTML opens in a sandboxed,
script-disabled viewer — never in the app's own origin.

This is the phase that makes the library feel permanent, and the one where "is it archived?" must have
an unambiguous answer per item.

## Scope

In: Worker render routes (HTML, screenshot, PDF), R2 writes for rendered artifacts, `archives` rows,
presigned viewing, the sandboxed viewer, screenshot as card image, PDF on demand, the re-archive/link
check scheduler, `link_status`, badges, content-hash churn control, archive failure as a retryable job.

Out: version history (latest only, M1), WARC, video archiving, cookie walls/authenticated archives,
archive of PDFs as HTML, replay of interactive pages.

## Deliverables

```
worker/src/render.ts        `POST /render {url, kinds}` (potato scope): Browser Rendering content() → HTML,
                            screenshot(), pdf() (on demand). Synchronous — the potato waits (S4 measures
                            the time; switch to accepted-then-poll only if it exceeds ~60 s)
worker/src/presign.ts       extend: archive keys, 5 min TTL
worker/migrations/<next>_renders.sql   transient `renders` records only; the potato's `archives` table is
                             the authoritative per-item record (numbered in merge order)
server/internal/archive/
  request.go    ask the Worker to render; retry; never block the item
  store.go      archives row, content_hash, screenshot key, pdf key
  check.go      the staggered link checker, registered in the in-process scheduler: >30 days old,
                ≤50/day, jitter; fetches and hashes the extracted text on the potato and asks the
                Worker to re-render only when that hash changed
  status.go     ok|dead|error transitions, keep-last-archive rules
server/internal/httpapi/archive.go   presigned redirect, "download original file"
web/src/routes/archive/[id]/+page.svelte    iframe sandbox="" + screenshot + PDF button
web/src/lib/components/LinkBadge.svelte     dead / paywalled / error
```

## Render rules

- **HTML:** single-file (monolith-style inlining: CSS, images as data URIs where size allows, fonts
  skipped), capped at 20 MB. Scripts stripped. A partial render (images failed) is still an archive.
- **Screenshot:** 1200×630, used as the card image when `og:image` is missing. Stored in R2 beside the
  HTML.
- **PDF:** generated on demand for the item page's "save as PDF" action; cached; 20 MB cap.
- **Never on the potato.** The Workers render; the potato asks and stores keys. The potato's egress is
  for fetching text and captions, not Chromium.
- **Failure is a job, not an item state.** A failed render leaves the item `ready` with no archive and
  a badge; retried 3× then parked.

## Tests first

1. Render request: the Worker accepts `{url, kinds:["html","screenshot"]}`, returns keys + content
   hashes, and a partial result (HTML ok, screenshot failed) is representable and does not fail the
   item.
2. `archives` upsert: one row per item; a second archive with an unchanged `content_hash` does **not**
   rewrite the row or the R2 object (assert on the fake R2 put count); a changed hash overwrites the row
   and writes a new object, leaving the old key deleted (or orphan-swept — **decide and test**).
3. Link check transitions: 200 → `ok`; 404/410 → `dead` + archive retained; 403/429/5xx/timeout →
   `error` + retry later; a `dead` item that comes back to life → `ok` again.
4. Scheduler: only items older than 30 days and not checked in the last 7 days; at most 50 per run;
   ordering is stable and staggered; a crash mid-run resumes without re-checking the same 50.
5. Sandbox: the viewer route renders an `<iframe sandbox="">` with no `allow-scripts`, no
   `allow-same-origin`; the CSP on that route forbids inline script; a fixture archive containing
   `<script>` and an `onerror` attribute is inert (assert by structure, and manually in the demo).
6. Presign: archive keys only, TTL ≤300 s, GET only, and an expired URL is refused.
7. Archive bytes never pass through the potato: assert the handler issues a redirect to the presigned
   URL rather than proxying (a test on the 302 and its `Location`).
8. A page that is 100% JS (empty `content()` before hydration) still yields a screenshot and a
   "reader text unavailable" state — the item is not broken.
9. **Checker budget:** 50 due items whose content is unchanged make 50 potato-side fetches and **zero**
   render requests; one changed page makes exactly one. A dead page (404) makes none.

## Tasks

- [ ] Worker render route with S4's session shape (one session for HTML + screenshot, PDF separately if
      needed) and per-stage timeouts.
- [ ] Single-file inlining: a small, dependency-light implementation on the Worker (or `monolith` via
      WASM if it fits the bundle budget — record the choice and size).
- [ ] `archives` table + migration; `content_hash` computed by the Worker over the rendered HTML.
- [ ] The render request is made right after fetch/extract and runs asynchronously; it never gates
      classify, tag, or embed (design §6 step 3). Archive failure is a retryable job, never a failed
      item.
- [ ] Card image preference order: `og:image` → screenshot → placeholder; never a broken image.
- [ ] Viewer route: screenshot first, "view archive" loads the sandboxed iframe on demand (it is heavy),
      "download HTML" and "download PDF".
- [ ] Link badge: dead (amber), paywalled (amber, from `is_paywalled`), error (amber, quieter). Dead
      badge appears on the card and the item page, with "archived on <date>" beside it.
- [ ] Link checker registered in the in-process scheduler (state in `task_runs`), staggered, capped,
      decoupled from rendering as described above, with a Status page line ("last link check", "next
      batch").
- [ ] Re-archive on manual Refresh reuses the same path and the same content-hash rule.
- [ ] R2 lifecycle: old archive keys on overwrite, orphan sweep extended to `archives/` prefix.
- [ ] Update design.md §11 with the exact caps, the retention rule for superseded archives, and the
      scheduler's parameters as built.

## Exit criteria

Automated: render/archive/checker tests green; sandbox structure test green; presign TTL test green;
`make check` green.

Demo:

1. Save a JS-heavy SPA and a static article → both get a screenshot; the static one gets a full HTML
   archive; the SPA's archive is a shell with a working screenshot and reader text unavailable.
2. Open the archive viewer on the SPA → the iframe runs no script, the page is visible, the app's own
   navigation is unaffected, and the CSP report shows no violations in the console.
3. Download the archived HTML → it opens standalone in a browser and looks approximately like the page.
4. Archive a page containing a `<script>` that would write a cookie → nothing happens; verify manually.
5. Backdate 60 items past 30 days and run the checker: ≤50 checked, statuses set correctly, a
   deliberately dead URL is badged amber, its archive still opens.
6. Re-archive an unchanged page → no new R2 object (assert from the log/put count), `updated_at`
   unchanged on `archives`.
7. Kill the Worker mid-render → the item is still `ready`, the archive badge says "not archived", and
   the job retries and succeeds after recovery.
8. Generate a PDF on demand → cached; second click is instant; the PDF renders in the sandboxed viewer.

## Risks

- **Browser Rendering cost/limits.** The free plan is 10 browser-minutes a day and 3 concurrent
  browsers. That is why the link checker fetches and hashes on the potato and renders only on change.
  S4's arithmetic is the budget; if it is tighter than assumed, the archive becomes weekly rather
  than per-save for low-value kinds, or screenshot-only. Record the decision in design.md.
- **Single-file HTML is a trap for size.** Inline images can explode the 20 MB cap; cap per-asset
  inlining (e.g. ≤200 KB each) and skip the rest, recording how many were skipped.
- **Sandbox escape** is the security boundary of this phase. No `allow-same-origin` with
  `allow-scripts`, ever; archives load from R2's own origin so even a mistake isn't same-origin with the
  library. A presigned R2 object comes back with only the headers stored on the object, so we cannot
  attach a CSP there: the controls are `sandbox=""` on the iframe, scripts stripped at render time, and
  the different origin. Test those three; do not claim a CSP the origin cannot send. The *app's* CSP
  must allow exactly the R2 host for `frame-src` and `img-src`.
- **Dead-link checking burns the site's goodwill.** Real UA, low volume, respect `Retry-After`, no
  retries on 4xx. 50/day is the cap for a reason.
- **Content hashes that change every render** (timestamps, nonces in the HTML) make every re-archive
  look like a change. Normalise volatile attributes before hashing; the churn test is the guard.
- **Archive orphaning.** Overwrites leave old keys; if the sweep doesn't know about `archives/`, storage
  grows silently. Extend the sweep and the Status page storage number.

## Build-time decisions to record

- Inlining implementation and bundle size on the Worker.
- Per-asset inline cap and total archive cap.
- Superseded-archive retention (delete immediately vs 30-day sweep).
- Scheduler parameters: age threshold, daily cap, re-check interval.
- Whether screenshots are re-taken on every re-archive or only when the page changed.
