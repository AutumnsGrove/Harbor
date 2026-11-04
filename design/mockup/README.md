# Harbor mockup

A clickable, dependency-free mockup of the whole product, with invented sample data. It is the
reference for the SvelteKit app: tokens, components, every screen, and every state the design doc
promises.

Open `index.html` in a browser (no build step; fonts and the Lucide icon script load from the web).
The page header switches between **Desktop** and **Phone**, turns the **soft amber** role on and off,
and the left index jumps to any screen. The app itself is responsive: under 720 px wide it becomes the
phone layout, whatever the frame.

```
design/
  directions.html        the three directions that were explored
  mockup/
    index.html           page shell: screen index, device switch, amber switch
    css/tokens.css       colour, type, radius, motion  → web/src/app.css
    css/components.css   buttons, chips, tags, forms, panels, menus, toasts
    css/screens.css      layouts for every screen, plus the .app.phone overrides
    js/data.js           sample items, kinds, Spaces, tokens, backups, status
    js/covers.js         stand-in cover art (real items use og:image / screenshots / uploads)
    js/ui.js             helpers, markdown, rule engine, overlays, action dispatch
    js/views-*.js        library, item + reader + archive, spaces + tags, settings
    js/main.js           hash router, resize, search, keyboard shortcuts
```

## Screens and routes

Routes are plain hashes so they survive a shared link.

| Route | Screen | Build step it belongs to (design.md §12) |
|---|---|---|
| `#home` | Search, Space chips, kind and tag/source/date filters, masonry, pending cards, bulk select | 2, 3, 9 |
| `#first-run` | Empty library: Import or paste a first link | 5 |
| `#attention` | Failed saves with retry | 3 |
| `#item-<id>` | One layout per kind (all 12), editable title/kind/note/tags, related, provenance | 3, 6, 7 |
| `#reader-<id>` | Reader with select-to-highlight, four colours, annotations, detached highlights | 8 |
| `#archive-<id>` | Sandboxed archive (iframe `sandbox=""`), screenshot, PDF on demand | 11 |
| `#spaces`, `#space-new`, `#space-edit-<id>` | Spaces list, AI suggestion, rule editor with live preview | 9 |
| `#tags` | Tag manager: rename, merge (leaves aliases), delete | 9 |
| `#prefs` | Settings hub with the update flow (check, update, health check, rollback note) | 0 |
| `#doors` | Sources and per-source write-only tokens: rotate, revoke | 4 |
| `#import` | Raindrop CSV and Netscape HTML, progress and summary | 5 |
| `#deleted` | Recently deleted, 30-day purge | 9 |
| `#data` | Export, storage, backups, restore (CLI only) | 12 |
| `#status` | Queue, mirror, backup, disk, link check | 12 |
| `#shortcuts`, `#system` | Shortcut list, design system | |

The composer is a modal on desktop and a sheet on phone (`n`, or the Save button). It detects an
already-saved link and says it will merge. Everything mutates in memory only; **Reset sample data**
reloads.

## Rules the mockup encodes

- Magenta marks what you can act on. Amber marks what is waiting on you. Teal marks healthy. Nothing
  else is coloured. `data-amber="off"` on `<html>` folds amber into magenta to prove the UI reads without it.
- One face, no borders, details on hover (desktop) or always visible (phone).
- A save never fails: pending, needs-attention and dead-link states are first-class.
- Destructive actions are confirmed and undoable; delete moves to Recently deleted.
- Restore is a CLI command, never a button.
- Every control is keyboard reachable and shows a magenta focus ring; motion stops under
  `prefers-reduced-motion`.

## Porting notes

- `tokens.css` becomes the app's root CSS variables unchanged.
- `V.<name>` views map to SvelteKit routes (`/`, `/item/[id]`, `/reader/[id]`, `/spaces/[id]`, `/settings/*`).
- `ruleTest` in `js/ui.js` is the reference for the Space rule vocabulary:
  `{tags_all, tags_any, kinds, sources, within, text, similar_to}`, fields ANDed, lists ORed.
- The search ranking, enrichment and sync are intentionally absent; this is the interface only.
