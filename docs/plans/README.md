# Harbor — build plans

**Status: planned. No product code yet.** These are the plans the build will follow. They do not
replace [design.md](../design.md); they say *how and in what order* the thing described there gets
built, and what "done" means for each step.

| Document | Role |
|---|---|
| [design.md](../design.md) | **The source of truth** for shape, stack, data model, and settled decisions. |
| [open-questions.md](../open-questions.md) | The decision record: every question, its answer, why the losers lost. |
| [README.md](README.md) (this file) | Sequencing, dependencies, invariants, and what "done" means. |
| [testing.md](testing.md) | The TDD + fuzzing contract every phase is held to. |
| [spikes.md](spikes.md) | The eight verifications that gate a phase, with pass conditions. |
| `00`–`12-*.md` | One plan per phase: goal, deliverables, tests first, tasks, exit demo, risks. |
| [design/mockup/](../../design/mockup/README.md) | The visual reference the UI is ported from. |

Suggested reading order before the first build session: this file → [spikes.md](spikes.md) →
[testing.md](testing.md) → [00-foundations.md](00-foundations.md).

If a plan and design.md disagree, **design.md wins and the plan is wrong** — fix the plan in the same
commit. If the *build* proves a design decision wrong, change design.md's decision log first, then
adjust the plan and the mockup. One direction of truth, never two.

---

## Build discipline (settled 2026-11-03)

Two decisions apply to every phase. They are not aspirational; a phase that skips them is not done.

### Test-driven development, for sure

- **Test first.** Every behaviour starts as a failing test. A commit that adds behaviour without the
  test that demanded it is rejected on review — same as a compile error.
- **The test is the spec.** For anything with a written rule in design.md (canonicalisation, merge,
  canonical tags, rule vocabulary, anchoring, retry budget), the test file is the executable version
  of that rule and cites the design section in a comment.
- **No mocking the thing under test.** SQLite tests run real `modernc.org/sqlite` against a temp
  dir; HTTP tests use `httptest`; Nova, R2, Telegram, and OpenRouter sit behind small interfaces with
  hand-written fakes. Fakes are in `internal/*/fakes.go`, not generated.
- **AI output is never asserted for content**, only for *shape*: a schema-valid response, a kind
  from the 12, 3–6 tags, a 1024-dim vector. Quality is measured by a scored fixture set, not by
  snapshot tests that break every prompt tweak.
- Refactor is a separate step from red→green, and it is not optional: after green, the code is
  cleaned up with the tests still running.

### Fuzzing, for sure

The library exists to swallow hostile input from the public internet — arbitrary HTML, PDFs, URLs,
Telegram updates, model JSON, filenames. That is exactly the shape fuzzing is for.

- **Engine: Go's native fuzzing** (`testing.F`, `go test -fuzz`). No external fuzz runner, no
  OSS-Fuzz in v1 — one toolchain, runs in the same `go test` invocation CI already has.
- **Properties: [`pgregory.net/rapid`](https://pkg.go.dev/pgregory.net/rapid)** for the invariants
  that are not "don't panic" but "always hold" (idempotence, merge commutativity, rule semantics) and
  for stateful model-based tests of the queue and the importer. This is the only fuzz/property
  dependency added to the Go module.
- **Frontend/Worker: `vitest` + [`fast-check`](https://fast-check.dev/)** properties for the same
  kinds of rules on the TS side (intake validation, Space rule evaluation).
- Every fuzz target commits its seed corpus to `testdata/fuzz/<FuzzName>/`. A crash found in CI
  becomes a seed file in the same commit as the fix — that is the regression suite.
- Fuzz targets are named per phase in the plans below. The full table and the CI budget live in
  [testing.md](testing.md).

---

## Invariants that no phase may break

These are checked at every exit gate. A phase that violates one is reverted, not patched around.

1. **The save path never fails.** Accepting a save does no parsing, no AI, no archiving, no network
   call beyond writing the row/blob. Enrichment failure is a badge, never a lost save.
2. **The potato is never a server to the public internet.** Outbound only. The Worker can *append*
   and can never read the library, enumerate it, or delete from it.
3. **`harbor.db` is the irreplaceable artifact.** Migrations are forward-only, a `VACUUM INTO`
   snapshot is taken before every update, and a failed update restores it automatically.
4. **Export always works.** From any shipped version, with no network, without the Worker or R2. It
   is tested in CI from phase 12 and manually at every release before that.
5. **No public read path exists.** Not a flag, not a token, not a "preview mode".
6. **Enrichment is replayable.** The original URL, note, and blob are stored before any model sees
   them, so kind/tags/summary/vectors can be recomputed without re-saving.
7. **Every external call has a timeout, a byte cap, and a retry budget.** No unbounded read, no
   unbounded retry, no unbounded page.
8. **One binary, one process, no Node at runtime.** The frontend is embedded at build time.

---

## Phase map

Dependencies are real: a phase starts when its predecessors' exit criteria pass, not when its own
code is written. "Gate" is the spike or verification that must be answered before the phase can
exit; the spike list is [spikes.md](spikes.md).

```
spikes ──► 00 foundations ──► 01 worker ──► 02 core library ──► 03 enrichment
                                                    │                  │
                                                    ├──► 04 doors ◄────┘
                                                    ├──► 05 import
                                                    ├──► 06 media ──► 07 transcripts
                                                    ├──► 08 reader
                                                    ├──► 09 tags + spaces
                                                    └──► 10 search (needs 03)
                                    11 archive + link rot (needs 01, 03)
                                    12 durability (needs 02; hardens through 12)
```

| # | Plan | Depends on | Gate | Phase exit demo |
|---|---|---|---|---|
| — | [spikes.md](spikes.md) | — | — | 8 verifications answered, each in ≤2 h |
| 0 | [00-foundations.md](00-foundations.md) | — | S7 | A deployed Harbor that updates and rolls itself back |
| 1 | [01-worker.md](01-worker.md) | 0 | S1, S2 | `curl` a save and an upload; nothing behind it yet |
| 2 | [02-core-library.md](02-core-library.md) | 1 | — | A save from Telegram/curl becomes an item you can open |
| 3 | [03-enrichment.md](03-enrichment.md) | 2 | S5 (smoke) | 20 real links organise themselves and are searchable |
| 4 | [04-doors.md](04-doors.md) | 2 (03 for quality) | — | Same save from four front doors, each attributed |
| 5 | [05-import.md](05-import.md) | 2, 9 (tables) | — | The real Raindrop library moves in, twice, safely |
| 6 | [06-media.md](06-media.md) | 2 | S6 | A Telegram photo and a 20 MB PDF are searchable |
| 7 | [07-transcripts.md](07-transcripts.md) | 3, 6 | S8 | A YouTube link is searchable by what is said |
| 8 | [08-reader.md](08-reader.md) | 3 | — | A highlight survives a re-extract, or says it can't |
| 9 | [09-tags-spaces.md](09-tags-spaces.md) | 3 | — | Merge two tags; re-import doesn't resurrect the old one |
| 10 | [10-search.md](10-search.md) | 3, 9 | S3 | "That article about the thing" in <150 ms at 10k items |
| 11 | [11-archive-linkrot.md](11-archive-linkrot.md) | 1, 3 | S4 | A JS-heavy page is readable offline; a dead link is honest |
| 12 | [12-durability.md](12-durability.md) | 2 | — | A restore drill on a clean box reproduces the library |

Ordering is deliberately *usable-first*: 0–3 is already a working link-and-note library. Everything
after adds v1 scope without ever putting itself in the save path. Phases 4–10 are parallelisable
across weekends; 12 runs alongside all of them.

---

## Progress

Tick a phase only when **all** of its exit criteria pass, its plan is updated with what actually
happened, and `make check` is green on `main`.

| # | Phase | State |
|---|---|---|
| — | Spikes | ☐ not started |
| 0 | Foundations | ☐ not started |
| 1 | Worker intake | ☐ |
| 2 | Core library | ☐ |
| 3 | Enrichment | ☐ |
| 4 | Front doors | ☐ |
| 5 | Import | ☐ |
| 6 | Images + documents | ☐ |
| 7 | Transcripts | ☐ |
| 8 | Reader + highlights | ☐ |
| 9 | Tags + Spaces | ☐ |
| 10 | Hybrid search | ☐ |
| 11 | Archive + link rot | ☐ |
| 12 | Durability + export | ☐ |

---

## Target repository layout

Nothing below exists yet except `docs/` and `design/`. This is what the phases build toward; each
phase plan names the parts it adds.

```
Harbor/
├── Makefile                 dev · web · build · test · fuzz · docker · deploy · check
├── Dockerfile               multi-stage, distroless, non-root, read-only rootfs
├── docker-compose.yml       project `harbor`, loopback 8895, ./data
├── .env.example             image digest, UID/GID, port, deployment mode
├── contracts/               JSON Schema + fixtures shared by Worker and potato
│   ├── save.schema.json     POST /save request
│   ├── job.schema.json      claim response (one job, with blobs)
│   └── fixtures/            cases both sides validate against
├── server/                  the Go binary
│   ├── cmd/harbor/          main: config, wiring, graceful shutdown
│   └── internal/
│       ├── build/           version stamp, env config
│       ├── httpapi/         routes, middleware, identity lock, static embed
│       ├── store/           sqlite open, migrations, every query
│       ├── model/           item, tag, space, media, highlight types
│       ├── canon/           URL canonicalisation (fuzzed)
│       ├── fetch/           capped HTTP client, content types, timeouts
│       ├── extract/         readability, markdown, PDF text (fuzzed)
│       ├── enrich/          Nova decide/chat/embed wrappers + prompts
│       ├── queue/           poll loop, claim/lease, retry, backoff
│       ├── media/           content addressing, thumbnails, mirror
│       ├── search/          FTS5 queries, vector cache, RRF
│       ├── spaces/          rule parse + evaluate (fuzzed)
│       ├── tokens/          per-source token hash/verify
│       ├── transcripts/     captions, published podcast transcripts
│       ├── backup/          VACUUM INTO, zstd, R2 upload, restore
│       └── workerclient/    the Worker's HTTP API, typed, contract-validated
├── web/                     SvelteKit, static output, embedded
│   ├── src/app.css          ← design/mockup/css/tokens.css, unchanged
│   ├── src/lib/             components, api client, stores
│   ├── src/routes/          / · /item/[id] · /reader/[id] · /spaces/… · /settings/…
│   └── embed.go             //go:embed all:build
├── worker/                  Cloudflare Worker (Hono), D1, R2, Browser Rendering
│   ├── src/index.ts         routes, auth, rate limit
│   ├── src/{queue,blobs,render,presign}.ts
│   ├── migrations/          D1 schema
│   └── test/                vitest + miniflare + fast-check
├── deploy/                  update watcher units, install.sh, security notes
├── docs/                    design, questions, plans, runbook
└── .github/workflows/       ci.yml (check), release.yml (multi-arch image)
```

**Package rule:** SQL lives in `store/`, network lives in `fetch/`/`enrich/`/`workerclient/`, and
nothing above `store` knows about `database/sql`. That is what keeps the fuzz targets small enough to
be meaningful.

---

## How to run a phase

1. **Read the plan and the design sections it cites.** If the plan is ambiguous, resolve it in the
   plan before writing code.
2. **Answer the phase's gate.** A spike that fails changes the plan, not the schedule.
3. **Write the failing tests first**, in the order listed under "Tests first" in the phase plan.
   Commit them red if they need a longer discussion; never commit behaviour without them.
4. **Build to green**, then refactor. Keep `make check` green at every commit.
5. **Run the exit demo for real**, on the potato, from the door a human would actually use.
6. **Update the docs in the same branch**: tick the phase here, record anything the build taught in
   [open-questions.md](../open-questions.md), and change [design.md](../design.md) if a decision
   moved.
7. **Update the mockup** if the UI diverged from it — the mockup is the visual reference and drift
   makes it useless.

## What "done" means globally

`make check` green (gofmt, vet, `go test -race`, fuzz smoke, `pnpm check`, vitest, Playwright smoke),
the phase demo run on the potato, the docs updated, and — from phase 0 onward — a container that has
survived at least one update-and-rollback cycle. A phase without a deploy is a prototype, not a
phase.
