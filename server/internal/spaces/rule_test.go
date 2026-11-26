package spaces

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// The rule vocabulary is the contract (open-questions H3), so each operator is
// asserted where it is turned into SQL, and the argument count is asserted against
// the placeholder count: a value that reached the string instead of the args would
// be an injection, and this is the test that notices.
func TestRuleCompilesToTypedSQL(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	days := 90

	cases := []struct {
		name  string
		rule  Rule
		want  []string
		args  []any
		sim   []string
		empty bool
	}{
		{
			name:  "an empty rule constrains nothing",
			rule:  Rule{},
			empty: true,
		},
		{
			name: "tags_all requires every tag",
			rule: Rule{TagsAll: []string{"rust", "go"}},
			want: []string{"t.name = ?", "t.name = ?"},
			args: []any{"rust", "go"},
		},
		{
			name: "tags_any requires one of them",
			rule: Rule{TagsAny: []string{"rust", "go"}},
			want: []string{"t.name IN (?, ?)"},
			args: []any{"rust", "go"},
		},
		{
			name: "kinds and tags_any AND across fields",
			rule: Rule{Kinds: []model.Kind{model.KindArticle, model.KindVideo}, TagsAny: []string{"ai"}},
			want: []string{"kind IN (?, ?)", "t.name IN (?)"},
			// Args follow the placeholder order in the generated SQL: the tag
			// subquery is emitted first, then the kind list.
			args: []any{"ai", "article", "video"},
		},
		{
			name: "sources",
			rule: Rule{Sources: []model.Source{model.SourceTelegram}},
			want: []string{"source IN (?)"},
			args: []any{"telegram"},
		},
		{
			name: "within_days is relative to now, not to the Space",
			rule: Rule{WithinDays: &days},
			want: []string{"saved_at >= ?"},
			args: []any{"2026-08-28T12:00:00Z"},
		},
		{
			name: "after is inclusive",
			rule: Rule{After: "2025-01-01"},
			want: []string{"saved_at >= ?"},
			args: []any{"2025-01-01"},
		},
		{
			name: "before is exclusive",
			rule: Rule{Before: "2025-01-01"},
			want: []string{"saved_at < ?"},
			args: []any{"2025-01-01"},
		},
		{
			name: "text goes through the escaped FTS builder",
			rule: Rule{Text: "long read"},
			want: []string{"items_fts MATCH ?"},
			args: []any{`"long" "read"`},
		},
		{
			name: "similar_to_item binds candidate ids, never a vector",
			rule: Rule{SimilarToItem: "abc"},
			want: []string{"id IN (?, ?)"},
			args: []any{"one", "two"},
			sim:  []string{"one", "two"},
		},
		{
			name: "similar to an item nothing resembles matches nothing",
			rule: Rule{SimilarToItem: "abc"},
			want: []string{"0"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clause, err := tc.rule.Where(context.Background(), now, fakeSimilarity{ids: tc.sim})
			if err != nil {
				t.Fatalf("Where: %v", err)
			}
			if tc.empty {
				if clause.SQL != "" || len(clause.Args) != 0 {
					t.Fatalf("clause = %+v, want no restriction", clause)
				}
				return
			}
			for _, fragment := range tc.want {
				if !strings.Contains(clause.SQL, fragment) {
					t.Errorf("SQL = %q, want it to contain %q", clause.SQL, fragment)
				}
			}
			if len(clause.Args) != len(tc.args) {
				t.Fatalf("args = %v, want %v", clause.Args, tc.args)
			}
			for i, want := range tc.args {
				if clause.Args[i] != want {
					t.Errorf("arg %d = %v, want %v", i, clause.Args[i], want)
				}
			}
			// The injection guard: one placeholder per bound value, and no value in
			// the string itself.
			if got, want := strings.Count(clause.SQL, "?"), len(clause.Args); got != want {
				t.Errorf("%d placeholders for %d args:\n%s", got, want, clause.SQL)
			}
			// The fragments never contain a quote, and a value would need one to be
			// interpolated into this SQL.
			if strings.Contains(clause.SQL, "'") {
				t.Errorf("a quote reached the SQL string: %s", clause.SQL)
			}
		})
	}
}

// Fields AND together: a rule with three fields has three fragments joined by AND.
func TestRuleFieldsAndTogether(t *testing.T) {
	clause, err := Rule{
		Kinds:   []model.Kind{model.KindArticle},
		Sources: []model.Source{model.SourceImport},
		Text:    "venice",
	}.Where(context.Background(), time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(clause.SQL, " AND "); got != 2 {
		t.Errorf("SQL = %q: %d ANDs for three fields", clause.SQL, got)
	}
}

// A rule that outlives the vocabulary it was written with must fail loudly. The old
// mockup names are the ones a stale client would send.
func TestParseRejectsUnknownFields(t *testing.T) {
	for _, raw := range []string{
		`{"within": 30}`,
		`{"similar_to": "abc"}`,
		`{"tags_all": ["x"], "colour": "red"}`,
		`{"tags_all": ["x"]} {"tags_all": ["y"]}`,
		`{"kinds": ["article"]`,
		`not json`,
	} {
		if _, err := Parse([]byte(raw)); !errors.Is(err, ErrInvalidRule) {
			t.Errorf("Parse(%s) = %v, want ErrInvalidRule", raw, err)
		}
	}
}

func TestParseNormalises(t *testing.T) {
	rule, err := Parse([]byte(`{
		"tags_all": ["  Rust ", "GO"],
		"tags_any": ["AI"],
		"after": "2025-01-01T23:30:00-05:00",
		"text": "  long read  "
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := strings.Join(rule.TagsAll, ","); got != "rust,go" {
		t.Errorf("tags_all = %q, want lowercased and trimmed", got)
	}
	if got := strings.Join(rule.TagsAny, ","); got != "ai" {
		t.Errorf("tags_any = %q", got)
	}
	// A timestamp becomes its UTC date: 23:30 on the 1st in New York is the 2nd in UTC.
	if rule.After != "2025-01-02" {
		t.Errorf("after = %q, want the UTC date", rule.After)
	}
	if rule.Text != "long read" {
		t.Errorf("text = %q, want it trimmed", rule.Text)
	}
}

func TestValidateRejectsWhatCannotBeEvaluated(t *testing.T) {
	zero := 0
	negative := -5
	tooMany := 99999
	tags := make([]string, MaxTags+1)
	for i := range tags {
		tags[i] = "tag"
	}

	cases := map[string]Rule{
		"unknown kind":       {Kinds: []model.Kind{"gadget"}},
		"unknown source":     {Sources: []model.Source{"carrier-pigeon"}},
		"zero days":          {WithinDays: &zero},
		"negative days":      {WithinDays: &negative},
		"absurd days":        {WithinDays: &tooMany},
		"unparseable date":   {After: "last tuesday"},
		"backwards window":   {After: "2025-06-01", Before: "2025-01-01"},
		"empty window":       {After: "2025-06-01", Before: "2025-06-01"},
		"too many tags":      {TagsAll: tags},
		"text that is a doc": {Text: strings.Repeat("x", MaxTextBytes+1)},
	}
	for name, rule := range cases {
		if err := rule.Validate(); !errors.Is(err, ErrInvalidRule) {
			t.Errorf("%s: %v, want ErrInvalidRule", name, err)
		}
	}
}

// A rule with similar_to_item and no similarity source is a programming error, not
// a rule that matches everything.
func TestSimilarityIsRequiredForAPeopleRule(t *testing.T) {
	_, err := Rule{SimilarToItem: "abc"}.Where(context.Background(), time.Now(), nil)
	if err == nil {
		t.Fatal("a rule with similar_to_item compiled without a similarity source")
	}
}

// The empty rule is legal: it is "everything, as a saved view".
func TestEmptyRuleIsLegal(t *testing.T) {
	rule, err := Parse([]byte(`{}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !rule.Empty() {
		t.Error("a parsed empty rule is not Empty")
	}
	if _, err := rule.Marshal(); err != nil {
		t.Errorf("Marshal: %v", err)
	}
}

type fakeSimilarity struct {
	ids []string
	err error
}

func (f fakeSimilarity) Neighbours(context.Context, string) ([]string, error) {
	return f.ids, f.err
}
