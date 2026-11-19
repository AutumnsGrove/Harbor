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
upload.schema.json   POST /upload — metadata from headers and the query string
job.schema.json      claim response — one job, with its blobs
fixtures/            cases both sides validate against
```

Each fixture is self-describing, so a new case needs no index edit:

```json
{ "schema": "save", "case": "empty body", "valid": false, "body": {}, "note": "why" }
```

`valid` is what the schema must say about `body`. The Worker's suite
(`worker/test/contracts.test.ts`) walks the directory with
`import.meta.glob`; the Go suite reads it at runtime from phase 2. If a fixture
and the schema disagree, the fixture is the spec until someone edits both.

The Worker imports the three schemas directly, so the deployed validator and the
fixtures cannot drift apart.
