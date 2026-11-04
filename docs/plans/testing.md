# Testing strategy — TDD and fuzzing

Two decisions, settled 2026-11-03: **test-driven development** and **fuzzing** are how Harbor is
built, not a cleanup pass at the end. This document is the contract for both. Every phase plan
references it and names the tests it owes.

The reason is the shape of the product, not principle: Harbor's inputs are arbitrary HTML, PDFs,
URLs, model output, and Telegram payloads arriving from the public internet, and its non-negotiable
property is that a save never fails. Neither of those is testable by clicking around.

---

## 1. The TDD loop

```
spec (design.md §)  →  failing test  →  minimal code  →  green  →  refactor  →  commit
```

- Behaviour is added test-first. A review question is not "are there tests?" but "which test
  demanded this?".
- One commit's worth of red, one of green, one of refactor if the cleanup is substantial. Squash on
  merge if the history is noise; never merge a red branch.
- Tests cite their source: `// design.md §5 — re-saving is a merge, not a duplicate`.
- Bug fixes start with a failing test that reproduces the bug. Always. This is where most of the
  regression suite will come from.
- When a design rule is ambiguous, the ambiguity is resolved in design.md *first*; the test then
  encodes the answer. A test that encodes an unresolved ambiguity is how two truths start.

### What is tested first, in practice

| Order | Layer | Why first |
|---|---|---|
| 1 | Pure rules: URL canonicalisation, merge, tag normalisation, Space rules, retry budget, anchor re-matching | Fast, deterministic, and they are the rules users actually notice |
| 2 | Store: migrations, constraints, cascades, soft delete, purge | Schema mistakes are expensive after data exists |
| 3 | Contract: Worker ⇄ potato JSON | Cross-language drift is invisible until deploy |
| 4 | Pipeline: fetch → extract → classify → index, with fakes | The order and the failure behaviour are the design |
| 5 | HTTP handlers | Thin; covered by the API-level tests |
| 6 | UI | Ported last, tested against the mockup |

---

## 2. Layers and where they live

```
   ┌ Playwright smoke (3 flows × 2 viewports) ── few, slow, run in CI on every PR
   ├ Vitest + fast-check (web rules, api client) ── fast, run on every save
   ├ Go handler/API tests (httptest, embedded UI) ── every PR
   ├ Go store tests (real sqlite, temp dir) ─────── every PR
   ├ Go property tests (rapid) ─────────────────── every PR, seeded
   └ Go fuzz targets (testing.F) ───────────────── 30 s smoke per PR · 10 min nightly
```

### Go (potato)

| Kind | Tool | Rule |
|---|---|---|
| Unit | `testing`, table-driven | Pure functions. No I/O. Subtests named for the rule they check. |
| Store | real `modernc.org/sqlite` in `t.TempDir()` | Apply real migrations. Never mock the DB. Assert constraints fail as designed. |
| Handler | `net/http/httptest` | Full middleware chain including `Host` check and identity lock. |
| Property | `pgregory.net/rapid` | Invariants over generated input; the only added test dependency. |
| Fuzz | `testing.F` + `go test -fuzz` | Untrusted-input boundaries only. Seeds committed. |
| Golden | `testdata/` + `-update` flag | Extraction output, exported Markdown, rendered reader text. Diff is the review. |

`go test -race ./...` is the default. Tests that need the network, a model, or Cloudflare are
`-short`-skipped and run only in the integration job with fakes.

### Web (SvelteKit)

- **`vitest`** for pure TS logic: the Space rule evaluator (ported from `design/mockup/js/ui.js`
  `ruleTest`, which is the reference vocabulary), URL/ID formatting, the API client's error mapping.
- **Testing Library** for components that carry state: composer merge warning, multi-select bulk bar,
  highlight colour picker, the pending/attention strips.
- **Playwright** for exactly three flows, desktop and 390 px: *save → pending → item*, *type → results*,
  *open item → edit → delete → undelete*. Screenshots compared at three widths against the mockup
  when a visual change is intentional.
- No component snapshot soup. If a test breaks on a class rename it was testing the wrong thing.

### Worker

- **`vitest` + `miniflare`** with real D1 and R2 bindings in-process — the worker gets the same
  "no mocking the thing under test" rule as the potato.
- **`fast-check`** for intake validation properties (below).
- **Contract tests**: load every file in `contracts/fixtures/`, validate against
  `contracts/*.schema.json` on both sides. Adding a field to a job means adding a fixture in the same
  commit, and the *other* language's CI fails first. That is the point.

### AI

- No test calls OpenRouter except the explicit, manually-run `make enrich-live` check.
- `enrich` sits behind an interface: `Decider`, `Chatter`, `Embedder`. Fakes return fixtures from
  `server/internal/enrich/testdata/`.
- Assertions are about **shape**: kind ∈ the 12 and never `unknown`; 3–6 tags; summary non-empty and
  ≤ 2 sentences' worth of bytes; vector length matches the recorded `dims`; JSON strictly parses.
- A **malformed-model-output suite** replays the ways models actually fail — truncated JSON, prose
  around JSON, an invented kind, 0 tags, 30 tags, a 1023-dim vector, an HTML-escaped apostrophe,
  `null` fields — and asserts the pipeline degrades (retry, then `failed`) instead of corrupting a
  row.
- Quality is tracked separately: `testdata/scored/` holds ~40 real saves with a human-assigned kind
  and tag set. `make enrich-score` prints accuracy. It is a report, not a gate — but a prompt change
  that drops accuracy by >10 points is a regression and gets reverted.

---

## 3. Fuzzing

### Engine and budget

- Engine: **Go native** (`testing.F`). Reason: zero extra tooling, runs anywhere `go test` does,
  corpus and crash inputs are ordinary files in the repo, and it is coverage-guided.
- Property/stateful testing: **`pgregory.net/rapid`**. Reason: the invariants that matter here are
  relational (`canonical(canonical(x)) == canonical(x)`, `merge(a,b) == merge(b,a)`,
  `import(import(x)) == import(x)`), and `rapid` also does model-based testing of the queue and
  importer, which native fuzzing cannot express.
- TS side: **`fast-check`**, same reasoning.
- Budget: each target **30 s** in the PR job (only targets whose package changed, plus a rotating
  third), **10 min** nightly for all. Fuzz time is cheap compared to a corrupted library.

### Targets

Every target's contract is: **no panic, no unbounded allocation, no unbounded loop, and the stated
property holds.** Seeds live in `testdata/fuzz/<FuzzName>/` next to the test.

| Target | Phase | Input | Property |
|---|---|---|---|
| `FuzzCanonicalURL` | 2 | arbitrary bytes/strings | never panics; output parses as a URL or is rejected; **idempotent**; scheme restricted to http/https; fragment always gone; `utm_*`/`fbclid` gone; non-tracking query preserved |
| `FuzzJobEnvelope` | 2 | `contracts/`-shaped JSON | unknown fields ignored or rejected consistently; missing required fields rejected; never panics; blob list bounded |
| `FuzzHTMLExtract` | 3 | HTML bytes | no panic; output is valid UTF-8; text length bounded by input × factor; a 5 MB cap is enforced *before* parse |
| `FuzzFTSQuery` | 3 | user search strings | never produces an SQL error or an injection; snippets are well-formed; empty/whitespace returns empty |
| `FuzzTelegramUpdate` | 4 | webhook JSON | dedupe by update id; album grouping by `media_group_id` terminates; wrong-token path never reaches the queue |
| `FuzzImportRow` | 5 | CSV/HTML row bytes | malformed rows are skipped with a warning, never abort the import; canonical URL idempotence holds per row |
| `FuzzPDFText` | 6 | PDF bytes | bounded page count and time; a malformed PDF is a typed error, not a hang or an OOM; extracted text is UTF-8 |
| `FuzzMediaKey` | 6 | sha256-ish strings | path traversal (`..`, `/`, NUL, unicode tricks) cannot escape `data/media` |
| `FuzzSpaceRule` | 9 | rule JSON | parses or is rejected; evaluation terminates; unknown operators rejected; `tags_all`/`tags_any`/`kinds` semantics match the reference evaluator |
| `FuzzAnchor` | 8 | anchor JSON + extraction text | re-anchor returns exact, unique-prefix, or detached — never a wrong span; ordinal bounds respected |
| `FuzzMarkdownNote` | 8 | Markdown bytes | sanitised output contains no `<script`, `on*=` or `javascript:`; round-trip is stable |
| `FuzzTranscriptPayload` | 7 | caption/vtt/json bytes | malformed payload sets `unavailable` and never fails the item |

`FuzzMediaKey`, `FuzzFTSQuery`, and `FuzzMarkdownNote` are the security-adjacent ones: they guard
file-write paths, SQL construction, and XSS respectively. They run on every PR, not only nightly.

### Properties (rapid), not targets

| Property | Phase | Statement |
|---|---|---|
| Canonicalisation | 2 | `c(c(u)) == c(u)`; two spellings of the same page map to one key |
| Merge | 2 | re-saving is idempotent; notes append in order; tags union; Read later membership survives; `updated_at` strictly increases |
| Bulk actions | 9 | applying tag/delete to N items is atomic (all or none) and equivalent to N single applications |
| Tag normalisation | 9 | rename→merge leaves an alias; a later write of the old spelling resolves to the canonical tag; no cycles |
| Import | 5 | one pass and two passes produce identical libraries |
| Queue | 2 | with N claims, retries, and crashes injected, every job is eventually processed exactly once *in effect*; a crashed claim is reclaimed after the lease |
| RRF ranking | 10 | result sets are deterministic for fixed input; a document present only in the keyword list still ranks; no duplicates |

### Corpus hygiene

- Seeds are small (a few KB) and committed. `testdata/fuzz` is *not* gitignored.
- Crash inputs found by CI are committed with the fix, and the target is run once in CI to prove the
  regression is captured.
- No corpus files containing personal data. Strip real URLs to `example.com` when minimising.
- `go test ./...` runs the seed corpus as ordinary tests on every PR, so even without `-fuzz` the
  regressions are live.

---

## 4. CI jobs

| Job | Trigger | Runs | Blocking |
|---|---|---|---|
| `check` | every push/PR | `gofmt -l`, `go vet`, `go test -race ./...`, `go test -run=Fuzz -fuzztime=100x ./...` (seed corpus) | yes |
| `fuzz` | every PR | 30 s per changed target + 3 security targets | yes |
| `fuzz-nightly` | cron | 10 min per target, all targets | opens an issue on failure |
| `web` | every PR | `pnpm check`, `pnpm test` (vitest + fast-check), `pnpm build` | yes |
| `e2e` | every PR | Playwright, 3 flows × 2 viewports | yes |
| `worker` | every PR | vitest + miniflare + fast-check; contract fixtures both directions | yes |
| `contract` | every PR | Go and TS validate `contracts/fixtures/` | yes |
| `no-outside-addresses` | every PR | grep the built frontend for off-host URLs (inherited from Warbler) | yes |
| `enrich-score` | manual/nightly with key | scored fixture accuracy report | no |
| `image` | push to `main` | multi-arch build + push, `latest` + short SHA | yes |

`make check` is the local equivalent of `check` + `web` + `worker`; if a developer's `make check` is
green, CI should be green.

---

## 5. Coverage policy

No global percentage gate — it invites tests written to move a number. Instead:

- **Pure-logic packages require ~90% line coverage**: `canon`, `spaces`, `search` (ranking math),
  `queue` (retry/lease), `tokens`, `backup` (path and retention logic). These are cheap to test and
  expensive to get wrong.
- **`store` requires every migration and every destructive path covered** (delete, purge, merge,
  cascade, alias).
- **Handlers and UI sit wherever the flow tests put them.** No target.
- Coverage is reported by `go test -coverprofile` and posted on the PR. A drop of >5 points in a
  pure-logic package fails review by policy, not by tooling.

## 6. What is deliberately not tested

- Pixel-perfect screenshots of every screen. Three flows, three widths, and the mockup as the human
  reference.
- Cloudflare's own behaviour (D1/R2 semantics beyond the spikes). We test *our* contract and our
  degraded path, not their uptime.
- Model quality as a hard gate. It is scored and reported, because a flaky model should not block a
  deploy of the save path.
- Migration *down*. Migrations are forward-only by design (design.md §3); the test is that an
  old database opens and upgrades, not that it can be downgraded.
