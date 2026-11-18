# contracts

JSON Schema and fixtures shared by two runtimes that cannot see each other's
types: the Cloudflare Worker (`worker/`, TypeScript) and the potato (`server/`,
Go).

**The rule: a field change lands with a fixture.** Adding, renaming or removing a
field means editing the schema *and* adding a case under `fixtures/` in the same
commit. Both sides validate every fixture in CI, so the language that was not
edited fails first. That is the point — cross-runtime drift is otherwise
invisible until deploy day (docs/plans/testing.md §2).

```
save.schema.json     POST /save  — a door's request body
job.schema.json      claim response — one job, with its blobs
fixtures/            cases both sides validate against
```

Schemas arrive with the Worker in phase 1. This directory exists from phase 0 so
the rule is not discovered later.
