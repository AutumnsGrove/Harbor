# Phase 7 — Transcripts: video and podcasts become readable

**Status:** planned · **Design refs:** [§6](../design.md#6-enrichment-pipeline) (video/podcast jobs),
[K4](../open-questions.md) · **Depends on:** phase 3 only (nothing here uses media) · **Gate:**
[S8](spikes.md#s8--video-transcript-source-from-the-potatos-ip) · **Est:** 1 evening + the spike

## Goal

A YouTube link is searchable by what is *said*, not just its title. Captions are fetched from the
potato (residential egress), stored in `transcript_text`, and fed to FTS5 and the embedding. A missing
transcript is a state, never a failure. Podcasts use a published transcript when one exists; no audio
transcription in v1.

This is the smallest phase and the most likely to be de-scoped by its own spike. That is fine — the
design is written so that "no transcript" is a normal, visible outcome rather than a broken feature.

## Scope

In: caption fetching (manual then auto-generated, language preference), VTT/SRT/JSON3 parsing into
plain text with paragraph breaks, storage + FTS + embedding, `transcript_state`, transcript display on
the video/podcast item layout, stale-transcript policy, published podcast transcripts via the feed
link.

Out: audio transcription, translation, speaker diarisation, chapter detection beyond what the page
provides, `yt-dlp` unless the spike says it is required.

## Deliverables

```
server/internal/transcripts/
  detect.go       host → provider (youtube, vimeo?, podcast feed link)
  youtube.go      id extraction (watch, youtu.be, shorts, embed), caption track discovery
  parse.go        VTT / SRT / json3 → []paragraph {start, end, text}
  podcast.go      fetch the episode page/feed, use a published transcript link if present
  testdata/       real payload shapes: manual, auto (no punctuation), multi-language, empty, malformed
web/src/routes/item/[id]/+page.svelte   transcript panel (collapsible, searchable, timestamp links)
```

## Tests first

1. **ID extraction**: `watch?v=`, `youtu.be/`, `/shorts/`, `/embed/`, with tracking params, with a
   timestamp, with a playlist — all yield the same video id. A non-video URL yields `no_transcript`
   rather than an error.
2. **Parsing**: VTT with cue ids and styling, SRT with CRLF, json3 with events and `segs`; output is
   plain text with paragraph breaks at ~30 s gaps and no duplicated rolling captions (the auto-caption
   overlap problem — assert by a fixture that repeats lines).
3. `FuzzTranscriptPayload`: arbitrary bytes never panic, never produce a 20 MB string from a 1 KB
   input, and malformed input sets `unavailable`.
4. **States**: `ok`, `unavailable` (no captions), `error` (network), and `too_large` (a 4-hour video's
   captions) all leave the item `ready` with a usable title/summary. One item, four outcomes — never a
   failed save.
5. **Search**: a phrase from a transcript finds the video; the transcript ranks below a body-text match
   by default (weight, not exclusion) — assert ordering with a fixed fixture ranking.
6. **Embedding**: transcript text is included in the embedding input, capped; changing the cap is a
   `prompt_version`-like migration, not a silent re-embed of everything.
7. **Language preference**: `en` preferred, falls back to the first available track, records which
   language was used (`transcript_lang`) so a later re-fetch can improve it.
8. **Podcast**: an episode with a transcript link uses it; one without sets `unavailable`; no audio
   download is attempted (assert on the fake HTTP client's requested URLs).

## Tasks

- [ ] Implement the S8 verdict exactly: whichever source won, with the fallback recorded in
      design.md §6 and the config that selects it.
- [ ] `transcripts` behind an interface, with a fake that serves the fixtures, so the pipeline tests are
      offline.
- [ ] The transcript columns (`transcript_text`, `transcript_state`, `transcript_lang`,
      `transcript_fetched_at`) already exist from `0001` (design §7), so no migration is needed;
      `transcript_state` takes `ok|unavailable|error|too_large`.
- [ ] Language preference config (`HARBOR_TRANSCRIPT_LANG=en`, comma list).
- [ ] Rate/etiquette: one request per job, identifying UA, no retries beyond the normal budget, and a
      per-host delay so a burst of 10 videos doesn't hammer YouTube.
- [ ] Cap stored transcript size (e.g. 500 KB) and truncate with `transcript_truncated=1` rather than
      refusing.
- [ ] Item layout: transcript panel, collapsible, with a "find in transcript" box that highlights
      matches, and timestamps linking to `?t=`. Empty state explains what's missing in one line.
- [ ] Re-fetch policy: `transcript_state='ok'` is not re-fetched on re-save unless older than 30 days;
      `unavailable` is retried at most once a month (auto-captions appear later on some videos).
- [ ] Update design.md §6 with the chosen source, the cap, and the retry policy.

## Exit criteria

Automated: parser + fuzz + state tests green; the fake-pipeline test proves all four outcomes keep the
item `ready`.

Demo:

1. Save five YouTube links, including one with no captions and one in a non-English language → four
   transcripts, one `unavailable` badge, all five items usable.
2. Search a distinctive phrase spoken 20 minutes into a video → the item is found with a snippet.
3. Open the transcript panel, click a timestamp → the video opens at that point (or, if embedding is
   out of scope, the YouTube link with `?t=` opens).
4. Save a podcast episode with a published transcript → text present; one without → `unavailable` and
   nothing downloaded.
5. Re-save the same video → no second caption fetch (assert in the log).
6. Record the real fetch success rate over the 10-video spike set and put the number in the S8 verdict.

## Risks

- **The caption endpoint is not a stable API.** It will break. The fallback ladder is: parse failure →
  `unavailable` (never fail the item) → a config flag to use `yt-dlp` if it was chosen in S8. The
  feature must degrade to "no transcript" without a code change overnight.
- **Auto-captions have no punctuation and rolling duplicates.** Parsing must dedupe overlaps or the
  transcript is unreadable and the embedding is noisy; the fixture set covers it.
- **Billing by transcript length.** A 4-hour video is a lot of embedding tokens; the cap is the guard.
- **Do not fetch from the Worker.** That was settled for a reason (datacenter IPs get blocked); the
  spike measures it. If someone later "simplifies" by moving it, the spike numbers are the argument
  against it.
- **Language detection is unreliable.** Store what was used rather than inferring; a wrong-language
  transcript is worse than `unavailable`, so only accept a track whose language matches the preference
  or the video's declared language.

## Build-time decisions to record

- Caption source and whether `yt-dlp` is in the image (adds ~40 MB and an update surface).
- Text cap and truncation behaviour.
- Whether the transcript feeds the summary prompt (recommend yes for video, with a token cap) or only
  the embedding.
- Re-fetch intervals for `ok` and `unavailable`.
