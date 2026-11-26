# Phase 3 — Enrichment: the library organises itself

**Status:** in progress — the pipeline, the Nova adapters, the per-kind item page and the first live
run all landed 2026-11-21; the scored fixture set still to come · **Design refs:** [§6](../design.md#6-enrichment-pipeline),
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

In: `fetch` (caps, timeouts, redirects, dial-time SSRF guard), `extract` (readability → Markdown +
plain text + JSON-LD details), the **tag write path** (normalise, resolve alias, reuse canonical —
moved here from phase 9 so import and enrichment share one implementation),
`enrich` (Nova `decide` for kind, chat for tags/summary/title, `embed` for vectors), prompt files with
versions, FTS5 indexing of extracted text and summaries, retry policy, attention strip, per-kind item
layouts (12 of them), scored fixture set.

Out: archives and screenshots (phase 11), images and PDFs (phase 6), transcripts (phase 7),
hybrid ranking (phase 10 — FTS ranking only here), Spaces (phase 9).

## Deliverables

```
server/internal/fetch/
  client.go        capped (5 MB), timed (20 s), limited redirects (5), no cookies, UA string
  guard.go         SSRF guard enforced in a net.Dialer.Control hook on the *resolved* address: refuse
                   loopback, RFC1918, link-local, 100.64.0.0/10 and fd7a:115c:a1e0::/48 (the tailnet),
                   0.0.0.0, IPv4-mapped IPv6, *.local, *.ts.net, and non-80/443 ports
server/internal/extract/
  html.go          go-readability + goquery → title, byline, published, text, markdown
  markdown.go      HTML → Markdown for the reader (phase 8 uses the same path)
  og.go            og:/twitter: metadata, cover image URL, site name
  jsonld.go        schema.org Recipe/Product/Book/Movie/Place/VideoObject/ScholarlyArticle →
                   details_json (the data behind the per-kind pages)
server/internal/enrich/
  enrich.go        the pipeline: fetch → extract → (request render, async, phase 11) → classify → tag →
                   embed → write. Notes (no URL) skip fetch/extract/classify but still get tags,
                   summary and an embedding
  classify.go      Nova decide, 12-way choice + noul (is_paywalled, is_video)
  tag.go           chat, strict JSON schema, prompt version
  embed.go         Nova embed, 1024 dims, normalise on write
  prompts/         classify.v1.md, tag.v1.md (+ a version constant read into items.prompt_version)
  fakes.go         fixture-backed Decider/Chatter/Embedder
  testdata/        recorded responses: happy, truncated, prose-wrapped, invalid kind, 0 tags, 31 tags
server/internal/store/
  search.go        FTS5 query builder (escaped), snippet(), ranking
  items.go         writes the enrichment columns that 0001 already created: summary, content_text,
                   content_md, details_json, og_json, tags_text, kind_source, prompt_version,
                   extract_version, enriched_at, and the retry columns (attempts, next_attempt_at, last_error)
  tags.go          normalise (trim, collapse whitespace, lowercase) → resolve alias (depth-capped) →
                   reuse canonical → insert; refreshes items.tags_text and the FTS row in one transaction
web/src/lib/components/
  KindBadge.svelte, AttentionStrip.svelte, SummaryBlock.svelte, TagRow.svelte
web/src/routes/item/[id]/+page.svelte   per-kind layout switch (12 branches)
```

## Columns (all created in `0001`)

No `ALTER TABLE` is needed here. `0001` (design §7) already carries `summary`, `content_text`,
`content_md`, `details_json`, `og_json`, `tags_text`, `kind_source`, `prompt_version`,
`extract_version`, `extract_error`, `enriched_at`, `attempts`, `next_attempt_at`, `last_error`,
`priority`. This phase is where they acquire meaning; `extract_version` ties to the highlight
anchor's `text_hash` in phase 8. (The first draft of this plan listed `summary`, `content_text` and
`og_json` as migration 0002 additions; they already exist, so that migration would have failed.)

## Tests first

**Fetch**

1. `fetch` refuses `localhost`, `127.0.0.1`, `::1`, `0.0.0.0`, `10.0.0.0/8`, `172.16/12`,
   `192.168/16`, `169.254/16`, **`100.64.0.0/10` and `fd7a:115c:a1e0::/48` (the tailnet itself)**,
   IPv4-mapped IPv6 forms of all of those, `*.local`, `*.ts.net`, non-80/443 ports, and bare `file:`
   (table test). This is a security boundary, not a nicety — the app fetches user-supplied URLs
   from inside the tailnet. The check runs in the dialer on the address actually connected to: a
   fake resolver that answers public on the first lookup and private on the second (DNS rebinding)
   must still be refused.
2. Byte cap: a response with `Content-Length: 50 MB` is refused before reading; a chunked response is
   cut at 5 MB and the error is typed (`ErrTooLarge`), not a panic.
3. Timeout: a server that sleeps 30 s yields `context.DeadlineExceeded` at ~20 s.
4. Redirect loop and cross-host redirect are both bounded; a redirect to a private IP is refused
   (the guard runs at dial time, so every hop is checked, not just the first URL).
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
16. **Retries live on the item.** `attempts`, `next_attempt_at` and `last_error` advance per failure;
    the Worker job is already `done` and is never touched again; after the third failure `state`
    is `failed`.
17. **Priority.** With an import-priority backlog of 500 queued items, a freshly saved link
    (priority 0) is enriched next. `rapid`: no interactive item is ever processed after a
    later-queued import item.
18. **Notes.** A URL-less note makes no fetch call and no classification call (assert on the fakes),
    keeps `kind='note'`, and still gets tags, a summary and an embedding.
19. **JSON-LD.** Recorded pages with Recipe, Product, Place and VideoObject markup populate
    `details_json` with the expected fields; a page without markup leaves `{}`; malformed JSON-LD is
    ignored, never fatal.
20. **Tag write path** (moved from phase 9): `"  Web   Dev "` → `web dev`; an existing canonical tag
    or alias is reused; a new tag is created only when none matches; unicode and emoji survive;
    alias chains are depth-capped and cannot cycle; `items.tags_text` and the FTS row update in the
    same transaction. Import (phase 5) and the enrichment prompt both call this one function.

## Tasks

- [x] `fetch` + `extract` behind one interface so tests can substitute a fixture body for a URL.
- [x] Fixture corpus in `server/internal/extract/testdata/pages/`: article, paywalled teaser,
      JS-only page (expect poor extraction — record it), non-Latin page, huge page, malformed HTML,
      PDF link. Golden output committed; `-update` regenerates.
- [x] Nova wiring: `decide` for kind, `provider/openrouter` + `deepseek/deepseek-v4.1-flash-1017` for
      tags/summary/title, `embed` for the vector. One shared HTTP client, per-call timeouts, token
      caps (8k in), and a usage log line with model + tokens + cost estimate.
- [x] Prompt files as embedded text with a version constant; the prompt includes the current canonical
      tag vocabulary (phase 9 depends on this habit being in place now).
- [x] Write path: item row updated in one transaction with FTS and embedding; `enriched_at` set.
- [x] `kind_source`: AI default, `manual` once a user edits it, and a manual kind is never overwritten
      by re-enrichment.
- [x] Per-kind item layouts, ported from `design/mockup` — each is a small switch over which
      `details_json` fields to show (recipe: ingredients/method; video: chapters/transcript
      placeholder; product: current price; paper: abstract + text-layer state; place: address/hours;
      …). The fields come from JSON-LD/microdata via `extract/jsonld.go`, not from the model. Build a
      generic layout plus article, video, recipe and product first; the other kinds follow as their
      data exists (see the cut list in plans/README). Price history/trends are deferred. Empty states
      are part of the work, not an afterthought.
- [x] Attention strip: failed items from `store` (not from logs), one-tap retry, dismissible for the
      session, amber per the colour rules.
- [x] The scored-set harness: `server/internal/enrichscore` (case loading, the label file, the
      scoring arithmetic, per-kind confusion, tag recall, the 80% gate) and
      `server/cmd/enrichscore` (`-record` captures through `fetch` + `extract`; the default mode
      scores against live Nova). `make enrich-score` / `make enrich-record`.
- [x] The scored set itself: 41 real saves captured and labelled; first scored run **95.1% kind
      accuracy** (39/41) against the 80% gate, 2026-11-22 (below).
- [x] Measured cost: the first live saves were logged and their real $/save is in design.md §6
      ($0.000788 for four saves, ~$0.0002 each). Re-measure after 100 real saves if the prompt changes.

## What landed (2026-11-21)

- **`fetch`**: 5 MB cap, 20 s timeout, five redirects, no proxy or cookies, and the SSRF policy
  checked on the URL *and* in the dialer on the resolved address (loopback, RFC1918, link-local,
  CGNAT, ULA/tailnet, NAT64, documentation ranges, ports other than 80/443, `*.local`, `*.ts.net`).
  `fetch.HTTPError.Permanent()` is what the retry policy reads.
- **`extract`**: one parse shared by go-readability and the Markdown converter, OG/Twitter
  fallbacks, curated schema.org details (Place subtypes fold into Place), output capped and forced
  to valid UTF-8. Ten golden fixtures plus `FuzzHTMLExtract`; fuzzing found a lone 0xE3 byte passing
  through the HTML parser, and that seed is committed.
- **Tags**: one write path for saves, import and AI — normalise, resolve aliases (depth 8,
  cycle-safe), reuse canon, replace per source, keep `tags_text` and FTS in the transaction. Manual
  tags survive AI re-enrichment.
- **Write**: `ApplyEnrichment` writes the row, AI tags, FTS and the 1024-dim vector in one
  transaction; a manual kind and a typed title are never overwritten.
- **Pipeline**: interfaces for fetch/decide/write/embed, a real store, and fakes with recorded
  answers. Notes skip fetch and decide; a manual kind skips decide; an unknown kind becomes
  `article`.
- **Scored set**: a case is a capture, not a URL, so scoring never depends on a page still being
  up and two runs see the same text. `enrich.PageText` with `ClassifyFor`/`ContentFor` is the one
  place the caps are applied, and both `Process` and the scorer go through it — otherwise the score
  measures a prompt nobody ships. Labels live in one `labels.json` (`"id": {"kind": "", "tags": []}`
  per line) that `-record` appends to and never overwrites; a blank kind is "not labelled yet" and
  is reported, not scored. A failed model call counts as **wrong**, because in production it is a
  failed save, and it also fails the run outright.
- **A capture the pipeline would refuse is not a case.** `-record` applies `enrich.IsHTML`, so a PDF,
  a JSON endpoint or a binary is a capture failure, not a scored save — production fails those
  permanently before extraction until phase 6. The PDF in the exit demo is the *item* side of that
  rule; it never belongs in the scored set. `labels` is refused as an id, because its capture would
  overwrite `labels.json`.
- **The tag vocabulary is a flag, not a default.** Production sends the library's canonical tags
  (`TagVocabulary(50)`) with the write call. The scored set has no library, so `-vocabulary FILE`
  takes one and the run prints which prompt it measured; without it the tag recall is a *fresh
  install's* number, not a mature library's, and is not comparable to one. Feeding the set's own
  human tags in would make the recall self-fulfilling, so it does not happen automatically.
- **The local key is pi's own** (`~/.pi/agent/auth.json`), read by the `enrich-score` make target and
  nowhere else: not by the server, not by CI, and exported rather than put on a command line.

Decisions made here:

- **Embedding input** is `title + summary + the first 2,000 characters of the body`; the hash of
  exactly that string is stored so re-embedding can be skipped later.
- **A summary is required**; more than 8 tags is a shape failure (retry, not truncation). 0 tags is
  allowed.
- **Two retries, then park**: 15 min then 45 min, three attempts in total; a 404, a blocked address,
  an oversized body, a non-HTML content type and a wrong-sized vector are terminal immediately.
- **PDFs are terminal until phase 6.** The item stays, the reason is `last_error`, and the attention
  strip (still to land) will show it.
- **The layout follows the data, not the label, in one case only.** If the item's kind is the
  `article` fallback and the page's JSON-LD names something more specific (Recipe, VideoObject,
  Product, Place, Book, Movie, ScholarlyArticle), the panel shows that shape. A deliberate kind is
  never overruled — someone who filed a product as a video sees the product facts they chose.
- **No cover and no media column yet.** Until phase 6 there is nothing to put in one, and the mockup
  only draws media when a cover exists. Phase 6 brings the column back alongside real images; the
  body components do not change.
- **The JSON-LD `description` is not rendered.** Enrichment always writes a summary, and two prose
  blocks that say the same thing is noise; papers keep their `abstract`.
- **Tags are read-only** until phase 9's tag editor, and the empty state says so. Cards still show no
  tags because the list DTO deliberately omits them.
- **Nova adapters** (`enrich/nova`) implement the three pipeline interfaces: `decide` with one
  choice question over the twelve kinds plus `paywalled`/`video` nouls; one strict JSON-schema chat
  call for title/summary/tags; `embed` through a role with `Dims: 1024`. Prompt files are embedded
  and named by version (`prompts/tag.v1.md` is `DefaultPromptVersion`), so the stored prompt version
  and the file cannot drift. Model ids are constants, not config: changing one is a code change with
  a new prompt version.
- **Wiring**: with `OPENROUTER_API_KEY` set, `serve` builds the real pipeline; without it the
  phase-2 shell stays and the log says enrichment is only marking items ready. A missing key never
  blocks a save.
- **Attention strip**: `store.FailedItems` reads the failed rows (not the logs, so a restart keeps
  them); `POST /api/items/:id/retry` resets attempts, clears the reason and requeues. Home shows an
  amber strip with up to three retryable failures, dismissible for the session.
- **Per-kind item page**: `details` (the parsed `details_json`) rides on every single-item response —
  GET, PATCH, restore, retry, create — and never on a list, so the grid stays one lean query. The
  page is one 860px column: title, kind picker, the model's summary, the kind panel, the note, tags,
  then provenance. `KindPanel.svelte` renders recipe ingredients/method, product price and
  availability, place address/hours, video length and link, book, film and paper fields, and an
  author/published/section line for everything else. The formatters (ISO 8601 durations, price,
  availability, rating, address, dates) live in `web/src/lib/details.ts` with vitest tests.
- **First live Nova run (2026-11-21)**: four real saves through `serve` with a key — a recipe page,
  two articles and a typed note — all reached `ready` on the first attempt, with no retries and no
  warnings except the expected "no Worker configured". The recipe classified as `recipe` and its
  panel fields arrived intact (`PT10M`/`PT20M`/`PT30M`, `Makes 12`, six ingredients, five steps).
  `typesafe/jev-1.14` is still a valid role model and served as the dated snapshot
  `typesafe/jev-1.14-20261014`; the Decisions endpoint accepted the two `noul` questions with no
  criteria; `deepseek/deepseek-v4.1-flash-1017` served itself and honoured strict `json_schema` (the
  answer parsed with no fence); `pplx-embed-v1-0.6b` returned 1024 dims, stored under its own name.
  Cost: **$0.000788 for four saves (~$0.0002/save)**, from $0.00012 for the recipe to $0.00030 for
  the note, whose summary ran long.

Still open: the scored fixture set.

## The live run, in detail (2026-11-21)

What was checked and what it showed, because these are the three questions that needed a real key:

- **The model is the cheap one (re-checked 2026-11-23).** `deepseek/deepseek-v4.1-flash-1017` is
  $0.03/M in and $0.20/M out on OpenRouter. The older dated snapshot
  `deepseek/deepseek-v4-flash-0731` costs **more** on every axis — $0.0434/M in, $0.30/M out, and
  $0.035/M for a cache read against $0.001 — so "the newer flash must be dearer" is backwards here and
  a 1,476-save import would cost about $3.20 on it instead of $2.20. Nothing changed. The levers that
  do matter are the ones already in the code: `MaxTokens: 800` on the write call and the 8k input cap.
- **A walled video is filed, not failed.** YouTube answers a datacenter fetch with Google's
  `/sorry/` wall (spike S8). The first real drain on the potato found it the hard way: 371 of 421
  video saves failed with that exact reason. `internal/youtube` now reads the public, keyless oEmbed
  endpoint for a video whose page could not be read, and the row is written with the video's title,
  `kind=video`, channel and still — state `ready`, `prompt_version='oembed.v1'`, no model call and
  no tags (there is no text to read, and phase 7 will redo those rows with a transcript). Tests:
  `internal/youtube` (URL shapes, answers, a fuzz target) and `internal/enrich` (a walled video with
  zero model calls; a non-video keeps its failure).
- **Model slugs are current.** `typesafe/jev-1.14` is accepted by the Decisions endpoint and is
  served as `typesafe/jev-1.14-20261014`; `deepseek/deepseek-v4.1-flash-1017` and
  `perplexity/pplx-embed-v1-0.6b` are served under the names requested. The usage log records both
  the requested and the served name, so a silent alias change is visible.
- **`noul` without criteria works.** Both `paywalled` and `video` were asked with only
  `Instructions`; the endpoint answered the whole question set and the call parsed.
- **Strict `json_schema` works on this path.** The write call returned a bare JSON object with
  exactly `title`/`summary`/`tags`, 3–6 tags, no fence and no prose, for all four saves.
- **The empty-string trap did not bite.** `enrich.New` still leaves `PromptVersion` and
  `EmbeddingModel` empty and `cmd/harbor` fills them from the `nova` constants; the rows record
  `tag.v1`, `extract.v1` and `perplexity/pplx-embed-v1-0.6b`, and the vectors are 1024 floats.

Per save, from the usage log (input tokens dominated by page text):

| save | classify | write | embed | total |
| --- | --- | --- | --- | --- |
| recipe page (6 ingredients, 5 steps) | $0.000057 | $0.000061 | $0.000002 | **$0.00012** |
| article, ~1.5k tokens in | $0.000066 | $0.000098 | $0.000002 | **$0.00017** |
| article, ~1.8k tokens in | $0.000074 | $0.000131 | $0.000002 | **$0.00021** |
| typed note (no fetch, no classify) | — | $0.000296 | $0.000001 | **$0.00030** |

The note is the expensive one because its summary was long (347 output tokens); the cap that matters
is `MaxTokens: 800` on the write call, not the page length.

## The first real drain (2026-11-23/24, on the potato)

The imported library — 1,476 saves, none of them ever read by a model — was queued with
`harbor enrich` and drained in about two hours for **$0.51** (Harbor's own OpenRouter key, $5/month).

- **954 enriched by the model**: a kind, 3–6 tags, a two-sentence summary and an embedding each
  (2,164 canonical tags, 10,626 assignments, 954 vectors). The model's own split (`kind_source='ai'`):
  article 638 · product 194 · video 51 · book 21 · movie 13 · paper 12 · image 11 · recipe 5 ·
  podcast 4 · place 3 · tweet 2 = 954.
- **522 failed**, and 371 of those were one cause: YouTube answers a datacenter fetch with Google's
  `/sorry/` wall. The oEmbed fallback (design §6) then filed **359 of them** from public metadata —
  real titles and channels, `kind=video`, the still as the cover, no model call, no tags — leaving
  **11 refused or gone** (oEmbed 401/403/404) and one broken affiliate redirect. Those 359 are the
  ones phase 7 re-enriches; the 12 that stayed failed have no captions to fetch and no `oembed.v1` row
  at all.

  Of those 11, **five were only refused** — 401/403 is what an owner who disabled embedding produces
  for a video that plays fine in a browser, and retiring them was wrong (issue #79). They are filed
  `ready` now, from the page's own `og:title` or the title the save already carried; only a 404, or a
  refusal with no title anywhere, still fails. That leaves **6 gone** — genuinely deleted — which is
  the number this phase's demo should show.
- **The whole library's kinds after the fallback** are article 801 · video 410 · product 194 · book 21
  · movie 13 · paper 12 · image 11 · recipe 5 · podcast 4 · place 3 · tweet 2. That list is not the
  model's: every imported row starts as the default `article`, so the failures sit inside it, and the
  359 filed videos are `kind=video` from their host rather than from the classifier.
- **163 in Needs attention afterwards**, down from 522: 151 non-video (PDF/JPEG/PNG links, a 5 MB
  fetch cap, Uniqlo/ASOS anti-bot, dead links — phases 6 and 11) and the 12 videos above.

That is the phase's demo at 1,476× rather than 20×, with one thing the owner still has to do: count
how many of the results she actually agrees with (criterion 2). Everything else in the demo list waits
on its own phase or on a fixture: keyword search is phase 10, the RSS measurement and the prompt-drift
re-enrich are still to be recorded.

## The scored set, and its first run (2026-11-22)

`server/internal/enrichscore/testdata/saves/` — 41 captures from 41 sources (`sources.txt`), ten of
the twelve kinds, five languages, two video hosts, repos and papers and shops, and the walls that
actually answer a plain fetch: 404 Media's member wall and the Guardian's registration wall stand in
for the paywalled teaser the demo names, because FT, WSJ, NYT and The Economist all **403** a fetch
without JavaScript — production cannot save them either.

- **Kind accuracy 95.1% (39/41)**, gate 80% → **pass**. Two misses, both defensible: Central Park's
  "Planning a Visit" page as `article` (wanted `place`), and a Wikimedia featured-pictures gallery as
  `article` (wanted `image`). Article is the documented catch-all, so these are the catch-all working.
- **Tag recall 44.7%** over all 41 — measured with **no vocabulary**, which is a fresh install's
  prompt (the flag exists so a mature library's number is measurable; feeding the set's own labels in
  by default would make the recall self-fulfilling).
- **Cost ≈ $0.06 for 41 saves**, about **$0.0015/save** — several times the $0.0002 in design §6;
  recorded there with the discrepancy rather than smoothed over.
- **`note` and `tweet` are absent and cannot be captured:** a note has no page by definition, and
  x.com serves nothing without auth. Ten kinds, not twelve, and the plan says so rather than faking
  two cases.

What the captures taught, kept as cases because production would really store them: a YouTube watch
page yields ~20,000 characters of `ytInitialPlayerResponse` JavaScript as its "article text", Vimeo
returns a Cloudflare challenge, and `www.thestrand.com` extracts nothing at all — no text, no excerpt,
no site name, and a title that is only the URL's host. Both videos were still filed as `video`, from
title and OG metadata. The Strand was filed as `place` from **the host in the URL alone**: one case,
and it is the one the phase's risk note is about — a bad capture still yields a usable item, but the
model is guessing.

Labels are the agent's reading of each captured page, one line each in `labels.json` and correctable
in a single edit. The exit demo's *"count what the human agrees with"* is still the owner's to run on
the potato; this set gates the prompt, not her taste.

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
- **SSRF.** The potato fetches arbitrary URLs from inside the tailnet. The dial-time guard is layer
  one; the host `DOCKER-USER` firewall rule from phase 0 is layer two (a hostname egress allowlist is
  impossible for an app that fetches arbitrary pages). Do not skip either.
- **Memory spike on parse.** Parse from disk, not from a `[]byte` of the whole body if avoidable; the
  acceptance test is peak RSS on the largest fixture, recorded.

## Build-time decisions to record

- Whether the summary/title/tag call is one JSON-schema call or three (design says one; keep one, and
  record the schema in the prompt file).
- The embedding input: title + summary + first N chars of content (cap it) — record N and why.
- Where `is_paywalled` is surfaced on the card versus the item page.

### Documents: Markdown and plain text (2026-11-26, issue #73)

A page can be worth reading and not be HTML. gwern.net answers Harbor with
`text/markdown; charset=utf-8` and a YAML front matter block, and the pipeline refused it as
`content type … is not HTML` — three real saves. `text/plain` was worse: it *was* accepted by the
HTML reader, so a text file went into a parser that found no article and failed permanently.

- `extract.Document` reads both: the body stays as it is in `content_text` **and** `content_md`,
  and Markdown syntax is deliberately not stripped. A hand-written stripper would be a second parser
  in the one place that must not lose text, FTS5's tokenizer already drops the punctuation, and a
  chat model reads Markdown natively. If search quality says otherwise, that is phase 10's
  measurement to make.
- Front matter is read as a **flat** `key: value` block — the static-site shape — and not as YAML.
  An unrecognised line is skipped rather than rejected, and an unclosed fence is not front matter at
  all (a file may simply start with a horizontal rule).
- `extract_version` is **not** bumped. The old behaviour for these types was a permanent failure, so
  no stored row was extracted under the old rule and none is stale; bumping would make 1,313 `ready`
  rows disagree with the constant without a re-read to justify it.
- The SSRF guard, the byte cap and the retry budget are unchanged. A PDF and an image still fail
  here, on purpose, until phase 6 gives them a reader.

### One line per item outcome (2026-11-26, issue #80)

A single failed save wrote three log lines at two levels — `WARN video metadata unavailable`, then
`WARN enrichment failed`, then `ERROR enrichment item failed` — so eleven parked rows made the tail of
`docker logs` read as an error storm while the state was unchanged and nothing was being attempted.

- One line per item, written by `fail()` in the pipeline, because that is the layer that knows
  whether the row was parked or scheduled: `INFO` for parked with a reason, `WARN` when a retry is
  pending, `ERROR` only when something broke the run itself (recording the failure, a dead store).
- The loop no longer writes its own copy; it counts. A video's metadata failure is no longer logged
  where it happens either, because the outcome above already carries it.
- The pass summary's two numbers belong to the pass. `failed` was a field that was never reset, which
  is how a pass that took 5 rows reported `failed:11` — every failure since the process started. Both
  counts are now returned by `Pass`, and `failed ≤ processed` is a property rather than a hope.

### An unreadable answer is retried at the call (2026-11-26, issue #78)

Eight rows spent all three item attempts on `nova write: answer is not JSON: unexpected end of JSON
input`, which is what an **empty** completion produces — and the message blamed JSON, so a provider
miss looked exactly like a page that will not read.

- `Write` now asks twice before the item is charged, and only for a *shapeless* answer. A transport
  failure is not retried here: that is the pipeline's retry to spend, and retrying it here would bill
  the dead network twice and halve the item's attempts for nothing.
- A `finish_reason` of `length` is **not** retried. The same input and the same cap cut the answer in
  the same place, so a second call pays for the same truncation; the item fails saying the answer hit
  the cap and naming the number to change (`writeMaxTokens`).
- The error now carries what the provider did — finish reason, completion tokens, answer length, and
  the first 80 bytes when it was prose — and how many calls were made. A parked item says `asked 2
  times`, so the next reader can tell one empty answer from a provider that gave nothing twice.
- Both calls are billed, so both are recorded in `usage_calls`; the cost of a retry is visible rather
  than absorbed.
