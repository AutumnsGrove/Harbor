# Phase 8 — Reader and highlights

**Status:** planned · **Design refs:** [§9](../design.md#9-ui-sveltekit-embedded) (Reader, highlights),
[A3](../open-questions.md), [H2](../open-questions.md), [L1/L3](../open-questions.md)
**Depends on:** phase 3 (extraction) · **Gate:** none · **Est:** a weekend

## Goal

Reading happens in the app: extracted article text in a quiet reader, select-to-highlight, an
annotation on each highlight, four colours, and a highlight list on the item. The hard problem is not
the UI — it is making a highlight survive re-extraction without ever silently pointing at the wrong
sentence.

Articles only in v1. PDFs and archived HTML are view-only.

## Scope

In: server-side Markdown/HTML rendering of extracted text, a reader route, selection → anchor capture,
anchor persistence, re-anchoring on load, detached highlights, four colours, annotations, highlight
list, delete/restore, reader typography and keyboard flow, mobile selection behaviour.

Out: PDF highlighting, highlights inside archived pages, sharing a highlight, export of highlights
(later, though phase 12's Markdown export will include them).

## Deliverables

```
server/internal/store/highlights.go     CRUD, by item, purge on item delete
server/internal/extract/render.go       items.content_md → sanitised reader HTML (goldmark + bluemonday;
                                         both named dependencies) + the normalised text version anchors use
server/internal/anchor/
  anchor.go        build(quote, prefix, suffix, ordinal, text_hash) · resolve(anchor, blocks)
  anchor_fuzz_test.go
server/internal/httpapi/highlights.go   POST/GET/PATCH/DELETE
web/src/routes/reader/[id]/+page.svelte
web/src/lib/reader/selection.ts         DOM selection → {quote, prefix, suffix, ordinal}
web/src/lib/reader/highlight.svelte     paint, four colours, click to annotate
web/src/routes/item/[id]/               highlight list + "read" entry point
```

## The anchor (the whole phase)

```json
{
  "quote": "the sentence the user selected",
  "prefix": "up to 40 chars before",
  "suffix": "up to 40 chars after",
  "ordinal": 2,
  "text_hash": "sha256 of the extraction version's text",
  "lang": "en"
}
```

**Definitions that the tests depend on.** Anchoring operates on one *normalised plain text* of the
article (whitespace collapsed, Unicode NFC, smart quotes folded) derived from `content_md`; the quote,
prefix and suffix are cut from that text, and the client sends the DOM selection's text through the
same normaliser. `ordinal` is the index of the occurrence **in the whole normalised text**, not
within a block. The block index the client reports is only a hint for speed.

Resolution order, from design.md H2, made explicit:

1. **Exact unique match** of `quote` → accept.
2. **Multiple matches** → use `ordinal` to pick the *n*th occurrence; if the ordinal is out of range,
   re-check with prefix/suffix before giving up.
3. **Unique prefix+quote+suffix** match (whitespace-normalised) → accept, and rewrite the stored anchor
   to the new positions' context.
4. **Nothing** → `detached`. The highlight is shown in a separate "couldn't re-anchor" group with the
   quote text and its note, never guessed onto a random span.

Never store character offsets or DOM paths as the primary anchor. That is the rule the fuzz test
enforces.

`text_hash` is the extraction version. When it changes, re-anchor *all* highlights for that item in one
pass, and mark any that fail as detached — with a one-line explanation, not an error.

## Tests first

1. `anchor.Resolve` table tests: exact; repeated quote with ordinal; repeated quote with a prefix that
   disambiguates; quote split across a paragraph boundary (should match after normalisation, and this
   is a **decision to record**); quote with different whitespace; quote with a changed word nearby
   (prefix mismatch → detached); quote deleted entirely (detached); empty quote (rejected at creation).
2. `FuzzAnchor`: arbitrary anchor JSON + arbitrary extraction text → never panics, never returns a span
   outside the text, never returns a match that does not contain the quote (after normalisation), and a
   detached result always carries a reason.
3. `rapid` property: for text T and a quote copy-pasted from T, resolution is exact. Insert text
   *before* T's quote → still exact or uniquely re-anchored, never wrong. Delete the quote → detached.
   Duplicate the whole paragraph → ordinal disambiguates.
4. Rendering: extraction → reader HTML contains no `<script>`, no `on*=` handlers, no `javascript:` URL
   (`FuzzMarkdownNote` covers the note path; the reader path gets the same assertions on fixtures).
   A `<figure>`/`<blockquote>`/code/list fixture renders sensibly.
5. Store: deleting an item cascades highlights; a highlight's `note` is editable; a colour change
   persists; highlights survive item edit (title/note change does not re-extract).
6. HTTP: creating a highlight with a quote not present in the current text is rejected (the client must
   send a real selection) — or accepted-and-detached if the extraction changed between render and save;
   **decide: accept and mark detached**, because losing a user's selection is worse than a detached one.
7. Selection → anchor on the client: a unit test on `selection.ts` with a synthetic DOM (jsdom) —
   forward and backward selections, selection spanning a highlight, selection of a single word, a
   selection ending mid-node.

## Tasks

- [ ] `extract/render.go`: blocks with stable ids so the client can report *where* a selection started
      (block index only, never character offsets as the anchor).
- [ ] Reader route: title, byline, date, reading width, the whole text; typography from the mockup
      (Schibsted Grotesk, generous leading, no serif); `prefers-color-scheme` irrelevant — dark only.
- [ ] Selection capture with a small toolbar (four colours + annotate) appearing above the selection,
      touch-friendly on phone.
- [ ] Highlight painting that survives re-render; clicking a highlight opens the annotation editor;
      deleting is explicit and confirmed (destructive, per §9 rules).
- [ ] Detached group at the end of the reader and on the item page: quote, note, "original text
      changed", delete.
- [ ] Item page: highlights list with quote + note + colour, sorted by position, each linking to the
      reader.
- [ ] Re-extraction hook: when `text_hash` changes, re-anchor in the enrichment job, set
      `highlights.detached` on each, and log the counts (`reanchored`, `detached`); the item page
      counts detached rows with a query, not a cached column.
- [ ] Markdown notes: render `items.note` with a preview toggle through the same sanitiser (phase 2
      shipped a plain textarea); `FuzzMarkdownNote` covers both the note path and the reader.
- [ ] Accessibility: selecting text with the keyboard (shift+arrows) works; the toolbar is reachable;
      the focus ring is magenta and visible; highlight colours have text/shape indicators, not colour
      alone.
- [ ] Reader is the default view for an article item; the archive link (phase 11) sits beside it.

## Exit criteria

Automated: anchor table + fuzz + property tests green; renderer XSS assertions green; selection unit
tests green; `make check` green.

Demo, on a phone and a desktop:

1. Open a long article; select a sentence; highlight it amber; add an annotation; reload → still
   highlighted, annotation intact.
2. Highlight the *same* sentence twice at different positions (duplicate paragraphs) → both resolve to
   the right occurrence after reload.
3. Trigger a re-extraction that inserts a paragraph above the highlight → highlight still lands on the
   right sentence.
4. Trigger a re-extraction that changes the sentence → the highlight moves to detached, is visible,
   carries its note, and is not painted on a wrong span.
5. Highlight 10 sentences on a phone with a thumb; the toolbar never covers the selection and the page
   does not jump.
6. Open a PDF item → no highlight affordance, view-only, clearly stated.
7. Delete a highlight → gone everywhere; undo is not offered (deletion of a highlight is cheap and
   explicitly confirmed) — if that feels wrong, that is a design change, record it.

## Risks

- **Anchor drift is silent when it fails quietly.** The detached state is the feature; make it loud
  enough to notice and quiet enough not to nag. One line, amber.
- **Re-extraction frequency.** Every Refresh/re-archive can change text. Re-anchor once per item per
  extraction change, not per highlight request.
- **Mobile selection UI.** iOS Safari fires selection events differently and its native menu competes
  with a custom toolbar. Test on the real device early; if it fights, use the native menu's action
  (via a positioned button) rather than a floating toolbar.
- **Reader HTML from a third-party page is untrusted.** Sanitise server-side, render with `{@html}`
  only on that sanitised payload, and keep the CSP as the second layer. An XSS here would run in the
  library's origin.
- **Highlight text is part of the export contract.** Phase 12 must include quotes + notes; don't design
  a store shape that can't be exported as Markdown blockquotes.

## Build-time decisions to record

- Quote spanning block boundaries: normalise and allow, or reject at creation.
- Whether the reader shows the original page's images (extraction gives URLs; they would be hotlinked —
  recommend: **no**, images come from the archive in phase 11).
- Detached-highlight retry: never automatic (only on the next extraction), or a manual "try again".
