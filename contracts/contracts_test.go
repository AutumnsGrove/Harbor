package contracts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// docs/plans/testing.md §2 "Contract tests" and plans/02 test 11 — the Worker
// validates every file in fixtures/ in vitest (worker/test/contracts.test.ts);
// Go validates the same files with the same expectations, or the two runtimes
// drift invisibly. A fixture is self-describing: {schema, case, valid, body}.

type fixture struct {
	File   string
	Schema string
	Case   string
	Valid  bool
	Body   any
}

func loadFixtures(t testing.TB) []fixture {
	t.Helper()
	paths, err := filepath.Glob("fixtures/*.json")
	if err != nil {
		t.Fatal(err)
	}
	var out []fixture
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		m, ok := doc.(map[string]any)
		if !ok {
			t.Fatalf("%s: fixture is not an object", path)
		}
		fx := fixture{File: filepath.Base(path), Body: m["body"]}
		fx.Schema, _ = m["schema"].(string)
		fx.Case, _ = m["case"].(string)
		fx.Valid, ok = m["valid"].(bool)
		if fx.Schema == "" || fx.Case == "" || !ok {
			t.Fatalf("%s: fixture needs schema, case and valid", path)
		}
		out = append(out, fx)
	}
	return out
}

// check is the whole rule: what Validate says about the body must equal what
// the fixture claims. It is a function so a test can prove it catches a flip.
func check(fx fixture) error {
	err := Validate(fx.Schema, fx.Body)
	switch {
	case fx.Valid && err != nil:
		return fmt.Errorf("%s (%s): want valid, got %v", fx.File, fx.Case, err)
	case !fx.Valid && err == nil:
		return fmt.Errorf("%s (%s): want invalid, but the schema accepted it", fx.File, fx.Case)
	}
	return nil
}

func TestFixturesMatchSchemas(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures) < 10 {
		t.Fatalf("found %d fixtures, want the full set", len(fixtures))
	}
	for _, fx := range fixtures {
		t.Run(strings.TrimSuffix(fx.File, ".json"), func(t *testing.T) {
			if err := check(fx); err != nil {
				t.Error(err)
			}
		})
	}
}

// The suite must fail if a fixture and a schema disagree, in both directions.
func TestCheckCatchesAFlippedFixture(t *testing.T) {
	for _, fx := range loadFixtures(t) {
		flipped := fx
		flipped.Valid = !fx.Valid
		if err := check(flipped); err == nil {
			t.Errorf("%s: flipping valid went unnoticed", fx.File)
		}
	}
}

func TestEveryFixtureNamesAKnownSchema(t *testing.T) {
	seen := map[string]int{}
	for _, fx := range loadFixtures(t) {
		seen[fx.Schema]++
	}
	for _, name := range []string{"save", "upload", "job"} {
		if seen[name] == 0 {
			t.Errorf("no fixture exercises the %q schema", name)
		}
		delete(seen, name)
	}
	for name := range seen {
		t.Errorf("fixture names unknown schema %q", name)
	}
}

func TestValidateRejectsUnknownSchemaName(t *testing.T) {
	if err := Validate("nope", map[string]any{}); err == nil {
		t.Error("Validate accepted a schema name that does not exist")
	}
}
