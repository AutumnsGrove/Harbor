package model

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/contracts"
)

// design.md §7 is authoritative for the shapes in this package, and
// contracts/job.schema.json for the enums that cross the Worker boundary. These
// tests read both files, so a change on either side that this package does not
// follow fails here instead of at deploy time.

const repoRoot = "../../../"

// ddlColumns returns the column names of one CREATE TABLE in design.md §7.
func ddlColumns(t *testing.T, table string) []string {
	t.Helper()
	raw, err := os.ReadFile(repoRoot + "docs/design.md")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(raw), "CREATE TABLE "+table+" (")
	if start < 0 {
		t.Fatalf("design.md has no CREATE TABLE %s", table)
	}
	rest := string(raw)[start+len("CREATE TABLE "+table+" ("):]
	end := strings.Index(rest, ");")
	if end < 0 {
		t.Fatalf("CREATE TABLE %s is not terminated", table)
	}
	var body []string
	for _, line := range strings.Split(rest[:end], "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		body = append(body, line)
	}

	var parts []string
	depth, cur := 0, strings.Builder{}
	for _, r := range strings.Join(body, " ") {
		switch {
		case r == '(':
			depth++
		case r == ')':
			depth--
		case r == ',' && depth == 0:
			parts = append(parts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	parts = append(parts, cur.String())

	var cols []string
	for _, part := range parts {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "CONSTRAINT":
			continue
		}
		cols = append(cols, fields[0])
	}
	sort.Strings(cols)
	return cols
}

func dbTags(v any) []string {
	var tags []string
	typ := reflect.TypeOf(v)
	for i := 0; i < typ.NumField(); i++ {
		if tag := typ.Field(i).Tag.Get("db"); tag != "" {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

func TestStructsMatchDesignDDL(t *testing.T) {
	tests := []struct {
		table string
		model any
	}{
		{"items", Item{}},
		{"tags", Tag{}},
		{"media", Media{}},
		{"highlights", Highlight{}},
		{"spaces", Space{}},
	}
	for _, tt := range tests {
		t.Run(tt.table, func(t *testing.T) {
			want, got := ddlColumns(t, tt.table), dbTags(tt.model)
			if !reflect.DeepEqual(want, got) {
				t.Errorf("columns differ\n design §7: %v\n model:      %v", want, got)
			}
		})
	}
}

func TestKindsAreTheTwelve(t *testing.T) {
	// design.md §6 step 4 and §13: the 12 kinds, and never 'unknown'.
	want := "article product book recipe video tweet note image paper podcast place movie"
	var got []string
	for _, k := range Kinds() {
		got = append(got, string(k))
		if !k.Valid() {
			t.Errorf("%q from Kinds() is not Valid", k)
		}
	}
	if strings.Join(got, " ") != want {
		t.Errorf("Kinds() = %v, want %s", got, want)
	}
	if Kind("unknown").Valid() || Kind("").Valid() {
		t.Error("'unknown' and the empty kind must not be valid")
	}
	if KindArticle != "article" || KindNote != "note" {
		t.Error("the default kinds drifted: URL items are 'article', URL-less notes 'note'")
	}
}

func TestEnumsRejectStrangers(t *testing.T) {
	if State("done").Valid() || Source("fax").Valid() || KindSource("auto").Valid() {
		t.Error("an enum accepted a value design §7 does not list")
	}
}

func TestEnumValuesMatchDesign(t *testing.T) {
	// design.md §7 comments, column by column.
	check := func(name string, got []string, want string) {
		t.Helper()
		if strings.Join(got, "|") != want {
			t.Errorf("%s = %v, want %s", name, got, want)
		}
	}
	check("State", names(States()), "queued|enriching|ready|failed")
	check("Source", names(Sources()), "app|telegram|shortcut|quick|share|import")
	check("KindSource", names(KindSources()), "default|ai|manual")
	check("TagSource", names(TagSources()), "ai|manual")
	check("MediaKind", names(MediaKinds()), "image|pdf|file")
	check("SpaceKind", names(SpaceKinds()), "auto|saved|manual")
	check("HighlightColor", names(HighlightColors()), "amber|magenta|teal|blue")
}

func names[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// plans/02 "model": every enum value is accepted by its schema where one
// exists. job.schema.json's `source` enum is the Worker's door list; each of
// those doors is a Source here, and a job carrying it validates.
func TestSourcesAgreeWithJobSchema(t *testing.T) {
	raw, err := os.ReadFile(repoRoot + "contracts/job.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Source struct {
				Enum []string `json:"enum"`
			} `json:"source"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties.Source.Enum) == 0 {
		t.Fatal("job schema lists no sources")
	}
	for _, door := range schema.Properties.Source.Enum {
		if !Source(door).Valid() {
			t.Errorf("job schema source %q is not a model.Source", door)
		}
	}
	// And the other way: every Source the Worker can deliver is accepted.
	for _, source := range []Source{SourceTelegram, SourceShortcut} {
		job := map[string]any{
			"id": "j1", "source": string(source), "kind": "link",
			"created_at": json.Number("1"), "blobs": []any{},
		}
		if err := contracts.Validate(contracts.Job, job); err != nil {
			t.Errorf("job from %q rejected by its own schema: %v", source, err)
		}
	}
	// The doors that never pass through the Worker must not validate as jobs.
	for _, source := range []Source{SourceApp, SourceQuick, SourceShare, SourceImport} {
		job := map[string]any{
			"id": "j1", "source": string(source), "kind": "link",
			"created_at": json.Number("1"), "blobs": []any{},
		}
		if contracts.Validate(contracts.Job, job) == nil {
			t.Errorf("source %q is local-only yet the job schema accepts it", source)
		}
	}
}
