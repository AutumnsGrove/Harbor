package contracts

import (
	"bytes"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// plans/02 test 11 and testing.md §3 — FuzzJobEnvelope. A claimed job crosses
// from the Worker to the potato as JSON; whatever arrives, validation must not
// panic, must agree with itself, and anything it accepts must really have the
// shape the ingest stage will rely on. (Tiered as a seed-corpus test plus the
// nightly run, not a PR fuzz budget: the bytes come from our own Worker.)
func FuzzJobEnvelope(f *testing.F) {
	// The committed corpus (testdata/fuzz/FuzzJobEnvelope) holds the five job
	// fixtures' bodies; these cover hostile shapes the fixtures do not.
	for _, seed := range []string{
		``, `null`, `[]`, `{}`, `"x"`, `42`,
		`{"id":"x","source":"telegram","kind":"link","created_at":1,"blobs":[]}`,
		`{"id":"x","source":"telegram","kind":"link","created_at":1.5,"blobs":[]}`,
		`{"id":"","source":"shortcut","kind":"note","created_at":1,"blobs":[]}`,
		`{"id":"x","source":"telegram","kind":"upload","created_at":1,"blobs":[{"n":-1,"key":"k","content_type":"image/png","bytes":1}]}`,
		`{"id":"x","source":"telegram","kind":"upload","created_at":1,"blobs":[null]}`,
		`{"id":"x","source":"telegram","kind":"link","created_at":1,"blobs":[],"extra":{"deep":[[[[]]]]}}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return // not JSON: the decode boundary rejects it before any schema runs
		}
		first := Validate(Job, doc)
		if second := Validate(Job, doc); (first == nil) != (second == nil) {
			t.Fatalf("validation is not deterministic for %q", data)
		}
		if first != nil {
			return
		}

		// Accepted: the required shape must really be there. Checked on the
		// parsed document, not through encoding/json, which would match field
		// names case-insensitively and disagree with the schema.
		job, ok := doc.(map[string]any)
		if !ok {
			t.Fatalf("accepted a non-object: %q", data)
		}
		if id, _ := job["id"].(string); id == "" {
			t.Fatalf("accepted a job with no id: %q", data)
		}
		if v, _ := job["source"].(string); v != "telegram" && v != "shortcut" {
			t.Fatalf("accepted source %v", job["source"])
		}
		if v, _ := job["kind"].(string); v != "link" && v != "note" && v != "upload" {
			t.Fatalf("accepted kind %v", job["kind"])
		}
		blobs, ok := job["blobs"].([]any)
		if !ok {
			t.Fatalf("accepted a job without a blobs array: %q", data)
		}
		for _, item := range blobs {
			blob, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("accepted a non-object blob: %q", data)
			}
			if key, _ := blob["key"].(string); key == "" {
				t.Fatalf("accepted a blob with no key: %q", data)
			}
		}
	})
}
