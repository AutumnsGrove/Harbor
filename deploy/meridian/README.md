# Meridian snippets

Ready-to-copy, but **merge, do not replace**: two of these three files are
fragments of larger ones on the potato, and pasting over the top would remove
every other service.

| File | Goes into | How |
|---|---|---|
| `tile.yaml` | `~/Meridian/config.yaml` | Paste under the matching `section:` key. Meridian watches the file. |
| `targets.json` | `/etc/meridian/targets.json` (root-owned) | Copy the single `"harbor": {…}` entry into the existing top-level object. |
| `observe.json` | `/etc/meridian/observe.json` | Add `harbor-update.path` to the existing `units` list; keep the others. |

After the allowlist change, run Meridian's doctor:

```bash
ssh potato-remote 'docker exec -i meridian /meridian doctor --allowlist -'
```

After adding the observed unit, press **Update** once from Meridian's `/control`
and confirm it appears in `~/Meridian/control/outbox/audit.jsonl`. The bare
`systemctl start harbor-update.service` path is the same path Settings uses; both
are exercised by `deploy/watcher/test.sh`.

Full explanation, including why the vital is `saved_this_week` and why the port
is 8897: [docs/meridian.md](../../docs/meridian.md).
