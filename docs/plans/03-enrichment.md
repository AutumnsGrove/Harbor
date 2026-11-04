# Phase 3 — Enrichment: the library organises itself

**Status:** planned · **Design refs:** [§6](../design.md#6-enrichment-pipeline),
[§8](../design.md#8-search) (FTS only), [C1–C5](../open-questions.md),
[§9](../design.md#9-ui-sveltekit-embedded) (attention strip, kind badges)
**Depends on:** phase 2 · **Gate:** [S5](spikes.md#s5--nova-embed-returns-what-designmd-says)
smoke only · **Est:** a weekend

## Goal

Saving becomes filing. Fetch, extract, classify into the 12 kinds, tag, summarise, embed — and make
the result searchable by keyword. Fully enriched items replace the placeholders from phase 2, the item
page gains its first real per-kind layouts, and failures become a visible "needs attention" strip with
one-tap retry.

This is the phase that proves the central bet: that the model's judgement is good enough to be the
default, and that a failure is always recoverable. It is also the first phase where cost per save is
real and is measured.

## Scope

In: `fetch` (caps, timeouts, redirects), `extract` (readability → Markdown + plain text),
`enrich` (Nova `decide` for kind, chat for tags/summary/title, `embed` for vectors), prompt files with
versions, FTS5 indexing of extracted text and summaries, retry policy, attention strip, per-kind item
layouts (12 of them), scored fixture set.

Out: archives and screenshots (phase 11), images and PDFs (phase 6), transcripts (phase 7),
hybrid ranking (phase 10 — FTS ranking only here), Spaces (phase 9).

## Deliverables

```
server/internal/fetch/
  client.go        capped (5 MB), timed (20 s), limited redirects (5), no cookies, UA string
  guard.go         SSRF guard: refuse loopback/RFC1918/link-local/.local hosts and non-80/443 ports
server/internal/extract/
  html.go          go-readability + goquery → title, byline, published, text, markdown
  markdown.go      HTML → Markdown for the reader (phase 8 uses the same path)
  og.go            og:/twitter: metadata, cover image URL, site name
server/internal/enrich/
  enrich.go        the pipeline: fetch → extract → classify → tag → embed → write
  classify.go      Nova decide, 12-way choice + noul (is_paywalled, is_video)
  tag.go           chat, strict JSON schema, prompt version
  embed.go         Nova embed, 1024 dims, normalise on write
  prompts/         classify.v1.md, tag.v1.md (+ a version constant read into items.prompt_version)
  fakes.go         fixture-backed Decider/Chatter/Embedder
  testdata/        recorded responses: happy, truncated, prose-wrapped, invalid kind, 0 tags, 31 tags
server/internal/store/
  search.go        FTS5 query builder (escaped), snippet(), ranking
  items.go         new columns: summary, content_text, og_json, prompt_version, kind_source
web/src/lib/components/
  KindBadge.svelte, AttentionStrip.svelte, SummaryBlock.svelte, TagRow.svelte
web/src/routes/item/[id]/+page.svelte   per-kind layout switch (12 branches)
```

## New columns (migration 0002)

`items.summary`, `items.content_text`, `items.og_json`, `items.prompt_version`, `items.kind_source`
(`ai|manual|default`), `items.enriched_at`, `items.extract_version` (ties to the highlight anchor's
`text_hash` in phase 8). Plus `items.extract_error`. Add them here rather than editing `0001` — this
phase is where they acquire meaning.

## Tests first

**Fetch**

1. `fetch` refuses `localhost`, `127.0.0.1`, `::1`, `10.0.0.0/8`, `172.16/12`, `192.168/16`,
   `169.254/16`, `*.local`, and bare `file:` (table test). This is a security boundary, not a
   nicety — the app fetches user-supplied URLs from inside the tailnet.
2. Byte cap: a response with `Content-Length: 50 MB` is refused before reading; a chunked response is
   cut at 5 MB and the error is typed (`ErrTooLarge`), not a panic.
3. Timeout: a server that sleeps 30 s yields `context.DeadlineExceeded` at ~20 s.
4. Redirect loop and cross-host redirect are both bounded; a redirect to a private IP is refused
   (the guard runs *after* redirect resolution).
5. `FuzzHTMLExtract`: no panic, valid UTF-8, bounded output, and the 5 MB pre-parse cap enforced.

**Classify / tag / embed**

6. Happy path with recorded fixtures: kind ∈ 12, never `unknown`; if the model returns something
   outside the set, it is coerced to `article` and logged — a test for exactly that.
7. Malformed-output suite (the table in [testing.md §2](testing.md#2-layers-and-where-they-live)):
   truncated JSON, prose around JSON, `null`s, 0 tags, 31 tags, HTML entities, an oversized summary.
   Every case ends in a retry or a typed failure — never a partially-written row.
8. Embedding: 1024 dims enforced against `item_embeddings.dims`; normalisation means `|v| ≈ 1` within
   float tolerance; a wrong-dimension response fails the job rather than writing a bad vector.
9. Idempotence: re-running enrichment on an unchanged item with the same `prompt_version` and
   `extract_version` does not call a model (content hash check) and does not duplicate FTS rows.
10. Prompt-version drift: changing `prompt_version` marks items as re-enrichable, and a "re-enrich"
    task picks them up — without touching `kind_source = manual`.

**Store / search**

11. FTS query escaping: `"`, `*`, `NEAR(`, unbalanced parens, `OR` at the end — never an SQL error.
    `FuzzFTSQuery` covers the same with generated bytes.
12. Search returns title/note/summary/content matches with a snippet that contains no unescaped HTML.
13. A soft-deleted item never appears in FTS results.

**Queue behaviour**

14. Retry 3× with backoff over ~an hour (time injected, not slept), then `failed` with `last_error`
    stored, and the item still exists, visible, with `state='failed'`.
15. A permanently bad URL (404) fails fast without consuming the whole retry budget (typed
    `ErrPermanent`), while a 503 retries.

## Tasks

- [ ] `fetch` + `extract` behind one interface so tests can substitute a fixture body for a URL.
- [ ] Fixture corpus in `server/internal/extract/testdata/pages/`: article, paywalled teaser,
      JS-only page (expect poor extraction — record it), non-Latin page, huge page, malformed HTML,
      PDF link. Golden output committed; `-update` regenerates.
- [ ] Nova wiring: `decide` for kind, `provider/openrouter` + `deepseek/deepseek-v4.1-flash-1017` for
      tags/summary/title, `embed` for the vector. One shared HTTP client, per-call timeouts, token
      caps (8k in), and a usage log line with model + tokens + cost estimate.
- [ ] Prompt files as embedded text with a version constant; the prompt includes the current canonical
      tag vocabulary (phase 9 depends on this habit being in place now).
- [ ] Write path: item row updated in one transaction with FTS and embedding; `enriched_at` set.
- [ ] `kind_source`: AI default, `manual` once a user edits it, and a manual kind is never overwritten
      by re-enrichment.
- [ ] Per-kind item layouts for all 12 kinds, ported from `design/mockup` — each is a small switch over
      which fields to show (recipe: ingredients/method; video: chapters/transcript placeholder;
      product: price row placeholder; paper: abstract + text-layer state; place: address/hours; …).
      Empty states are part of the work, not an afterthought.
- [ ] Attention strip: failed items from `store` (not from logs), one-tap retry, dismissible for the
      session, amber per the colour rules.
- [ ] Scored fixture set: ~40 real saves with human kind + tag sets; `make enrich-score` prints
      accuracy and per-kind confusion.
- [ ] Measured cost: log and record the actual $/save from the first 100 real saves; put the number in
      design.md §6's cost paragraph.

## Exit criteria

Automated: golden extraction diffs reviewed; malformed-model suite green; `FuzzHTMLExtract` and
`FuzzFTSQuery` seeds green; `make enrich-score` ≥80% kind accuracy with the recorded prompt.

Demo:

1. Save 20 real links from different corners (news, a recipe, a YouTube video, a GitHub repo, a shop
   page, a paywalled article, a non-English page, a PDF link).
2. Within a minute each has a kind, 3–6 tags, a summary, and a clean title. Count how many the human
   agrees with — that number is the phase's real result.
3. Keyword-search a phrase that only exists in the article body → the item is found with a snippet.
4. Save a URL that 404s → item exists, amber "couldn't fetch" strip appears, one-tap retry works
   after the fixture server starts serving it.
5. Stop the Nova key (bad token) → items still save, still appear, and enrichment failures queue
   visibly; no save is lost.
6. Re-enrich one item after editing the prompt version → tags change, `kind_source=manual` items keep
   their kind.
7. `docker stats` during a 10-item burst: RSS stays under the box's comfort line (record the peak).

## Risks

- **Readability on JS-heavy pages will be bad.** That is expected; phase 11's archive is the backup,
  and the item still exists with a title and OG image. Do not over-tune extraction here.
- **Kind drift with prompt edits.** The scored set and `kind_source=manual` are the guards. Never let
  a prompt change silently rewrite a manual kind.
- **Cost surprise from long pages.** Cap input tokens, log every call's usage, and record the real
  $/save; if it exceeds the estimate, the cap is wrong, not the model.
- **SSRF.** The potato fetches arbitrary URLs from inside the tailnet. The guard is tested, and the
  container's egress allowlist (phase 0) is the second layer. Do not skip either.
- **Memory spike on parse.** Parse from disk, not from a `[]byte` of the whole body if avoidable; the
  acceptance test is peak RSS on the largest fixture, recorded.

## Build-time decisions to record

- Whether the summary/title/tag call is one JSON-schema call or three (design says one; keep one, and
  record the schema in the prompt file).
- The embedding input: title + summary + first N chars of content (cap it) — record N and why.
- Where `is_paywalled` is surfaced on the card versus the item page.
