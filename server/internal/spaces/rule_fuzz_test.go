package spaces

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// FuzzSpaceRule — plans/09 test 2. A rule arrives as JSON, possibly from a client
// that has never seen this vocabulary, so the property is: never panic, always
// terminate, and when it compiles, the SQL has exactly one placeholder per bound
// value. A value that lands in the string instead of the args is an injection, and
// counting placeholders is what catches it.
func FuzzSpaceRule(f *testing.F) {
	seeds := []string{
		`{}`,
		`{"tags_all":["rust"],"tags_any":["go"],"kinds":["article"],"sources":["import"]}`,
		`{"within_days":90}`,
		`{"after":"2025-01-01","before":"2026-01-01"}`,
		`{"text":"long read"}`,
		`{"similar_to_item":"abc"}`,
		`{"within":30}`,
		`{"kinds":["nonsense"]}`,
		`{"tags_all":["a'b"],"text":"\" OR 1=1 --"}`,
		`{"text":"日本語 検索"}`,
		`[]`,
		`null`,
		``,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}

	now := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, raw []byte) {
		rule, err := Parse(raw)
		if err != nil {
			if !errors.Is(err, ErrInvalidRule) {
				t.Fatalf("Parse(%q) = %v, want ErrInvalidRule", raw, err)
			}
			return
		}

		clause, err := rule.Where(context.Background(), now, fakeSimilarity{ids: []string{"one", "two"}})
		if err != nil {
			return // a valid rule with an unusable text clause (pure punctuation) is fine
		}
		if got, want := strings.Count(clause.SQL, "?"), len(clause.Args); got != want {
			t.Fatalf("%d placeholders for %d args from %q:\n%s", got, want, raw, clause.SQL)
		}
		// The generated SQL is built from fragments that never quote anything, so a
		// quote in it would mean a value was interpolated. (Looking for the value
		// itself is a false positive: a tag named "1" collides with `SELECT 1`.)
		if strings.Contains(clause.SQL, "'") {
			t.Fatalf("a quote reached the SQL string for %q:\n%s", raw, clause.SQL)
		}
	})
}
