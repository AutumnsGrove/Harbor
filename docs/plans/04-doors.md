# Phase 4 — Front doors: the gesture works everywhere

**Status:** planned · **Design refs:** [§5](../design.md#5-save-path),
[D1–D4](../open-questions.md), [§11](../design.md#11-operations) (tokens)
**Depends on:** phase 2 (3 for a library worth saving into) · **Gate:** none ·
**Est:** 1 evening each

## Goal

Four ways in, all attributed: the **Telegram bot** and the **iOS Shortcut** (public, through the
Worker), the **tailnet pages** — the quick-save page and the Android PWA share target (tailnet-only,
posting straight to the potato) — and the in-app composer (done). After this phase the gesture works
from the phone lock screen, from the iOS share sheet, and from a couch bookmark — without a tunnel,
an open port, or a browser extension. Only the Shortcut and Telegram work with the VPN off; the
tailnet pages are *meant* to fail visibly there (see Risks).

This phase handles links and text end to end. Telegram photos, documents and albums are accepted and
stored by the Worker here, but the potato does not claim `upload` jobs until phase 6 (the claim
`kinds` filter), and their exit demo lives in phase 6.

## Scope

In: dedicated Telegram bot (owner check, webhook, links, text, photos, documents, albums, dedupe), the
Worker's webhook and `/admin/tokens` routes, door-token rotation/revocation in Settings → Sources, PWA
manifest + icons + `share_target`, a documented Shortcut recipe, the tailnet quick-save page, the
potato's `/api/quick` and `/share` endpoints.

Out: extensions, email-to-save, Raycast/Alfred, bookmarklet (all dropped, design.md §13).

## Deliverables

```
worker/src/telegram.ts       webhook verify (secret token header) + owner check, update → job
worker/src/admin.ts          /admin/tokens: list (no secrets), rotate, revoke — potato scope only
worker/src/telegram-media.ts getFile → stream to R2, album assembly by media_group_id
worker/test/telegram.*.test.ts  + FuzzTelegramUpdate equivalent (fast-check)
server/internal/tokens/      client for the Worker's /admin routes: list, rotate, revoke (plaintext is
                             shown once at creation and never stored on the potato)
server/internal/httpapi/doors.go   GET/POST /api/doors/:id/{rotate,revoke}
server/internal/httpapi/quick.go   POST /api/quick (source 'quick') · POST /share (source 'share', form POST)
web/static/manifest.webmanifest, icons/, share_target action → /
web/src/routes/quick/+page.svelte   one input, one button, works on 390px
web/src/routes/settings/doors/+page.svelte   per-source token state, rotate, revoke, copy once
docs/shortcut.md             step-by-step Shortcut recipe with the exact field mapping
```

## Telegram specifics (the fiddly part)

- **Dedicated bot**, own token, own webhook secret. Never Mira's.
- **Owner only.** A bot username is public. Before any D1 write or reply, the Worker compares the
  sender's Telegram user id with the `TELEGRAM_OWNER_ID` secret; anyone else gets a bare 200 and
  nothing else happens (no job, no reply — a reply would confirm the bot exists to a stranger).
- Webhook auth: Telegram's `X-Telegram-Bot-Api-Secret-Token` compared in constant time against the
  hash stored in the `telegram` token row (so Settings can show last-seen and revoke it like any
  other door). A wrong secret is a 401 with no D1 write — test it.
- **Dedupe by `update_id`.** Telegram retries; a retried update must not create a second job. Store
  the last-seen update id per chat, or a `telegram_updates` table in D1 with a unique constraint. The
  latter is simpler and testable — **decide: table**.
- **Albums** arrive as separate updates sharing `media_group_id`, with no "album complete" signal, and
  the webhook calls may run concurrently. The Worker has no durable timer, so: the first update of a
  group creates the job with `group_key = "<chat_id>:<media_group_id>"` (unique index `jobs_group`);
  later updates upsert on that key and append a blob row whose `n` is the Telegram `message_id` —
  **caller-supplied, never "count + 1"**, so two simultaneous updates cannot race for the same index —
  and bump `touched_at`. The claim query holds back only jobs with a `group_key` whose `touched_at`
  is within the last ~10 s; an ordinary save is never delayed. The potato orders blobs by `n`.
  (Already recorded in design §13.)
- **Text with no URL** → a note. Text with a URL → a link with the text as the note. Caption on a
  photo → the note.
- **Documents** → `/upload` with the filename as the title; the content-type allowlist applies, and a
  rejected type replies to the user in Telegram ("that file type isn't supported yet") instead of
  failing silently. Files over **20 MB** (the Bot API download ceiling, not our 25 MB cap) get a reply
  naming that limit. Telegram is the one door with a back channel — use it for errors.
- **Replies**: a short confirmation ("saved") is worth the API call; it is the only feedback on the
  gesture. Keep it to one message per job, no per-album noise.

## Tests first

1. `FuzzTelegramUpdate` (fast-check on the TS side): generated updates never 500; unknown update types
   are ignored with a 200 (Telegram retries on non-200); malformed JSON is a 400.
2. Dedupe: the same `update_id` twice → one job.
3. Album: three photo updates with one `media_group_id` → one job, three blobs, blob keys distinct
   (`n` = `message_id`). Interleaved groups (two albums at once) do not cross-contaminate, and three
   updates delivered **concurrently** still produce three distinct blobs (fast-check shuffles
   arrival order). A held album is not claimable until ~10 s after the last blob; an ordinary job is.
4. Media: a 21 MB document is rejected and the user gets a reply naming the 20 MB limit; an image is
   streamed to `incoming/<job>/<n>` and the job carries `content_type` and `bytes`.
5. Token lifecycle: rotate returns a new plaintext exactly once, the old token stops working
   immediately, revoking one door leaves the others working, and `GET /api/doors` never returns a
   secret (assert on the JSON shape). Rotation and revocation go through the Worker's `/admin/tokens`
   with the potato credential; a door token on `/admin` is 403.
6. Quick-save: `POST /api/quick` (tailnet-only, no bearer) writes locally with `source='quick'` and
   skips the Worker — it is on the potato already. It must work with the Worker unreachable.
7. Share target: a form POST to `/share` (the manifest's `action`) with `url`, `text`, `title` fields
   is accepted and saved with `source='share'`; a missing `url` with a `text` URL inside it is
   extracted (share sheets are inconsistent). It is tailnet-only: no bearer is accepted or needed.
8. PWA: manifest is valid (name, short_name, start_url, display, icons ≥192 and 512, `share_target`
   with `method: POST`, `enctype: multipart/form-data`), and there is **no** service worker registered
   (a test asserting the absence, so nobody adds one "for offline" without a decision).

## Tasks

- [ ] Bot creation documented (BotFather), token into `.dev.vars`/Worker secret, `TELEGRAM_OWNER_ID`
      set (find it once with the bot's `/start` echo in a dev run, then remove the echo), webhook set
      to the Worker URL with a secret; `docs/doors.md` records the setup steps.
- [ ] Worker: Telegram webhook, `/admin/tokens`, and a new migration adding `telegram_updates`
      (unique `update_id`) — numbered in merge order, not reserved. The potato's claim sends
      `kinds=link,note` until phase 6.
- [ ] Webhook route + secret check + dedupe table + album assembly + reply on error.
- [ ] `/upload` path for Telegram documents and photos, reusing the phase 1 streaming route.
- [ ] Settings → Sources: `telegram` (its row holds the hash of the webhook secret, so revoking it
      disables the webhook) and `shortcut`; rotate and revoke through the Worker's `/admin/tokens`
      with a confirmation dialog; plaintext shown exactly once. There is no `quick` token.
- [ ] PWA manifest, icons (maskable), theme colour from tokens, `display: standalone`, share target.
- [ ] Tailnet quick-save page: one field, paste or type, Enter saves, shows a toast and the pending
      card. Optimised for a phone on a home screen. No token in it (it is tailnet-gated).
- [ ] `docs/shortcut.md`: the Shortcut steps, the exact JSON it posts, and the failure mode when the
      token is wrong (a Shortcut that fails silently is the thing that kills trust — include a
      "test" step in the recipe that shows a notification).
- [ ] Settings → Sources shows last-seen-per-door (`tokens.last_seen_at`, for the two public doors;
      local `quick`/`share` saves are visible from `items.source`), so a silently broken door is
      visible.
- [ ] Check design §5/§13 still match what shipped (they already record the album hold, the dedupe
      table and the owner check); fix the doc and the code together.

## Exit criteria

Automated: Worker telegram suite + fast-check properties green; token lifecycle tests green; PWA
manifest test green; no service worker.

Demo, on real devices — this phase cannot be signed off from a terminal:

1. Share a link from Safari on the phone via the Shortcut → item appears, source `shortcut`.
2. Share a link from an Android browser via the PWA share target (VPN on) → item appears, source
   `share`; with the VPN off it fails visibly, never silently.
3. Send a link and a text note to the bot → two items, each source `telegram`, each with a sensible
   title, and a one-line "saved" reply. (Photos, documents and albums are demoed in phase 6.)
4. Add the quick-save page to the iPhone home screen; save from the couch with the VPN off (it should
   fail *visibly*, not silently); with the VPN on it works.
5. Rotate the `shortcut` token; the old Shortcut fails (visibly, per the recipe) and the new one works;
   Telegram keeps working throughout.
6. Send a 30 MB video to the bot → a friendly rejection reply naming the 20 MB limit, no job row, no
   orphan.
7. Message the bot from a second Telegram account → no reply, no job row, no D1 write.

## Risks

- **Telegram album debounce vs the poll loop.** A job with a `group_key` whose `touched_at` is inside
  10 s must not be claimed. That logic lives in the Worker's claim query, not in the potato, so it is
  one place and it is testable. It keys on `group_key` and `touched_at` — never on `updated_at`,
  which a claim or lease reclaim would bump, and never on ordinary saves.
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
