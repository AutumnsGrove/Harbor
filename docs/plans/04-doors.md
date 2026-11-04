# Phase 4 — Front doors: the gesture works everywhere

**Status:** planned · **Design refs:** [§5](../design.md#5-save-path),
[D1–D4](../open-questions.md), [§11](../design.md#11-operations) (tokens)
**Depends on:** phase 2 (3 for a library worth saving into) · **Gate:** none ·
**Est:** 1 evening each

## Goal

Four ways in, all writing to the same queue and all attributed: the Telegram bot, the iOS Shortcut /
PWA share target, the tailnet quick-save page, and the in-app composer (done). After this phase the
gesture works from the phone lock screen, from any share sheet, and from a couch bookmark — without a
tunnel, an open port, or a browser extension.

## Scope

In: dedicated Telegram bot (webhook, links, text, photos, documents, albums, dedupe), a Worker route
for the webhook, per-source tokens with rotation/revocation in Settings → Sources, PWA manifest +
icons + `share_target`, a documented Shortcut recipe, the tailnet quick-save page, `/api/doors`
rotation endpoints.

Out: extensions, email-to-save, Raycast/Alfred, bookmarklet (all dropped, design.md §13).

## Deliverables

```
worker/src/telegram.ts       webhook verify (secret token header), update → /save or /upload
worker/src/telegram-media.ts getFile → stream to R2, album assembly by media_group_id
worker/test/telegram.*.test.ts  + FuzzTelegramUpdate equivalent (fast-check)
server/internal/tokens/      hash/verify, rotate, revoke, list (no plaintext after creation)
server/internal/httpapi/doors.go   GET/POST /api/doors/:id/{rotate,revoke}
web/static/manifest.webmanifest, icons/, share_target action → /
web/src/routes/quick/+page.svelte   one input, one button, works on 390px
web/src/routes/settings/doors/+page.svelte   per-source token state, rotate, revoke, copy once
docs/shortcut.md             step-by-step Shortcut recipe with the exact field mapping
```

## Telegram specifics (the fiddly part)

- **Dedicated bot**, own token, own webhook secret. Never Mira's.
- Webhook auth: Telegram's `X-Telegram-Bot-Api-Secret-Token` compared in constant time. A wrong secret
  is a 401 with no D1 write — test it.
- **Dedupe by `update_id`.** Telegram retries; a retried update must not create a second job. Store
  the last-seen update id per chat, or a `telegram_updates` table in D1 with a unique constraint. The
  latter is simpler and testable — **decide: table**.
- **Albums** arrive as separate updates sharing `media_group_id`, with no "album complete" signal.
  Assembling them needs a short debounce. The Worker has no durable timer, so: on the first update of a
  group, write the job + blob; on subsequent updates within the group, append blobs to the same job by
  upserting on a `(chat_id, media_group_id)` natural key. The potato then waits until the job's
  `updated_at` is older than ~10 s before claiming it, so a half-arrived album is never enriched.
  **Record this in design.md §5** — it is the one place a delay is deliberately introduced.
- **Text with no URL** → a note. Text with a URL → a link with the text as the note. Caption on a
  photo → the note.
- **Documents** → `/upload` with the filename as the title; the content-type allowlist applies, and a
  rejected type replies to the user in Telegram ("that file type isn't supported yet") instead of
  failing silently. Telegram is the one door with a back channel — use it for errors.
- **Replies**: a short confirmation ("saved") is worth the API call; it is the only feedback on the
  gesture. Keep it to one message per job, no per-album noise.

## Tests first

1. `FuzzTelegramUpdate` (fast-check on the TS side): generated updates never 500; unknown update types
   are ignored with a 200 (Telegram retries on non-200); malformed JSON is a 400.
2. Dedupe: the same `update_id` twice → one job.
3. Album: three photo updates with one `media_group_id` → one job, three blobs, blob keys distinct.
   Interleaved groups (two albums at once) do not cross-contaminate.
4. Media: a 26 MB document is rejected and the user gets a reply; an image is streamed to
   `incoming/<job>/<n>` and the job carries `content_type` and `bytes`.
5. Token lifecycle: rotate returns a new plaintext exactly once, the old token stops working
   immediately, revoking one door leaves the others working, and `GET /api/doors` never returns a
   secret (assert on the JSON shape).
6. Quick-save: `POST /api/save` (tailnet-only, no bearer) writes locally and skips the Worker —
   it is on the potato already. It must work with the Worker unreachable.
7. Share target: a form POST to `/` with `url`, `text`, `title` fields is accepted; a missing `url`
   with a `text` URL inside it is extracted (share sheets are inconsistent).
8. PWA: manifest is valid (name, short_name, start_url, display, icons ≥192 and 512, `share_target`
   with `method: POST`, `enctype: multipart/form-data`), and there is **no** service worker registered
   (a test asserting the absence, so nobody adds one "for offline" without a decision).

## Tasks

- [ ] Bot creation documented (BotFather), token into `.dev.vars`/Worker secret, webhook set to the
      Worker URL with a secret; `docs/doors.md` records the setup steps.
- [ ] Webhook route + secret check + dedupe table + album assembly + reply on error.
- [ ] `/upload` path for Telegram documents and photos, reusing the phase 1 streaming route.
- [ ] Per-source token model in Settings: Telegram (managed by the bot), `shortcut`, `quick`; rotate
      and revoke with a confirmation dialog; plaintext shown exactly once.
- [ ] PWA manifest, icons (maskable), theme colour from tokens, `display: standalone`, share target.
- [ ] Tailnet quick-save page: one field, paste or type, Enter saves, shows a toast and the pending
      card. Optimised for a phone on a home screen. No token in it (it is tailnet-gated).
- [ ] `docs/shortcut.md`: the Shortcut steps, the exact JSON it posts, and the failure mode when the
      token is wrong (a Shortcut that fails silently is the thing that kills trust — include a
      "test" step in the recipe that shows a notification).
- [ ] Settings → Sources shows last-seen-per-door (from the jobs table), so a silently broken door is
      visible.
- [ ] Update design.md §5 and §13 with the album debounce and the dedupe table.

## Exit criteria

Automated: Worker telegram suite + fast-check properties green; token lifecycle tests green; PWA
manifest test green; no service worker.

Demo, on real devices — this phase cannot be signed off from a terminal:

1. Share a link from Safari on the phone via the Shortcut → item appears, source `shortcut`.
2. Share a link from an Android browser via the PWA share target → item appears, source `shortcut`.
3. Send a link, a text note, a photo, and a 3-photo album to the bot → four items (the album is one
   item with three media rows), each source `telegram`, each with a sensible title.
4. Add the quick-save page to the iPhone home screen; save from the couch with the VPN off (it should
   fail *visibly*, not silently); with the VPN on it works.
5. Rotate the `shortcut` token; the old Shortcut fails (visibly, per the recipe) and the new one works;
   Telegram keeps working throughout.
6. Send a 30 MB video to the bot → a friendly rejection reply, no job row, no orphan.

## Risks

- **Telegram album debounce vs the poll loop.** A job with `updated_at` inside 10 s must not be
  claimed. That logic lives in the Worker's claim query (`AND updated_at < now-10`), not in the potato,
  so it is one place and it is testable.
- **Share-sheet variability.** iOS sends `url` + `text`; Android may send only `text` containing the
  URL. Parse defensively and test both shapes.
- **Token in a Shortcut is plaintext on the device.** Accepted: it can only append. Say so in
  `docs/shortcut.md` and keep revocation one tap away.
- **Tailnet-only quick page will fail when the VPN is off.** That is by design (no public token in
  it). The visible failure is the feature; do not "fix" it by adding a token.
- **PWA on iOS is not the same PWA.** Share target is Android/Chrome only on iOS it won't appear —
  which is exactly why the Shortcut exists. Document it so the missing feature doesn't look like a bug.

## Build-time decisions to record

- Dedupe store: `telegram_updates` table vs last-id column (recommend the table).
- Album wait window (recommend 10 s) and where it is enforced.
- Whether the bot replies with the AI title after enrichment (recommend no — one ack, no second
  message, to keep the gesture quiet).
