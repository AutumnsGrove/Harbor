// Package enrichscore measures the enrichment prompt against real saves with
// human labels (plans/03, "Scored fixture set").
//
// A case is a page that was captured through Harbor's own fetch and extract, so
// scoring a case never depends on the network and two runs see the same text.
// The human label is the answer the prompt is measured against; the model is
// only ever asked at scoring time, because an assertion on model output would
// break on every prompt edit (plans/testing.md).
package enrichscore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AutumnsGrove/Harbor/server/internal/model"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// Threshold is the phase-3 exit criterion: at least 80% kind accuracy against
// the recorded prompt (plans/03, "Exit criteria").
const Threshold = 0.80

// labelsFile is the one hand-edited file in a scored set. It is keyed by case
// id, so a whole set can be labelled without opening a file per save.
const labelsFile = "labels.json"

// Case is one real save: the page as extract saw it, plus the human label.
// Kind and Tags are empty until the owner labels the case; an unlabelled case
// is recorded but not scored.
type Case struct {
	ID       string
	URL      string
	Captured string
	Title    string
	SiteName string
	Excerpt  string
	Text     string
	Kind     model.Kind
	Tags     []string
}

// Answer is one model prediction for one case. Err is the call's failure: an
// item that could not be enriched is a failed save in production, so it counts
// as wrong and never disappears into a denominator.
type Answer struct {
	ID   string
	Kind model.Kind
	Tags []string
	Err  error
}

// Row is one case's result.
type Row struct {
	ID       string
	URL      string
	Want     model.Kind
	Got      model.Kind
	WantTags []string
	GotTags  []string
	Err      error
}

// KindRow is one labelled kind's result and what it was mistaken for.
type KindRow struct {
	Labelled int
	Correct  int
	Confused map[model.Kind]int
}

// Report is one scoring run.
type Report struct {
	Labelled   int
	Unlabelled int
	Correct    int
	Accuracy   float64
	Kinds      map[model.Kind]KindRow
	Tagged     int
	TagRecall  float64
	Errors     []string
	Rows       []Row
}

// Score compares answers with labels. Cases are matched by id; a labelled case
// with no answer is wrong, and a case the owner has not labelled yet is only
// counted as unlabelled.
func Score(cases []Case, answers []Answer) Report {
	byID := make(map[string]Answer, len(answers))
	for _, answer := range answers {
		byID[answer.ID] = answer
	}

	report := Report{Kinds: map[model.Kind]KindRow{}}
	tagTotal := 0.0
	for _, c := range cases {
		if c.Kind == "" {
			report.Unlabelled++
			continue
		}
		report.Labelled++
		row := Row{ID: c.ID, URL: c.URL, Want: c.Kind, WantTags: c.Tags}
		report.Rows = append(report.Rows, row)
		index := len(report.Rows) - 1

		kind := report.Kinds[c.Kind]
		if kind.Confused == nil {
			kind.Confused = map[model.Kind]int{}
		}
		kind.Labelled++

		answer, ok := byID[c.ID]
		switch {
		case !ok:
			report.Rows[index].Err = errors.New("no answer")
			report.Errors = append(report.Errors, fmt.Sprintf("%s: no answer", c.ID))
		case answer.Err != nil:
			report.Rows[index].Err = answer.Err
			report.Errors = append(report.Errors, fmt.Sprintf("%s: %v", c.ID, answer.Err))
		default:
			got := answer.Kind
			report.Rows[index].Got = got
			report.Rows[index].GotTags = normaliseTags(answer.Tags)
			if got == c.Kind {
				kind.Correct++
				report.Correct++
			} else {
				kind.Confused[got]++
			}
			if len(c.Tags) > 0 {
				report.Tagged++
				tagTotal += overlap(row.WantTags, report.Rows[index].GotTags) / float64(len(c.Tags))
			}
		}
		report.Kinds[c.Kind] = kind
	}

	if report.Labelled > 0 {
		report.Accuracy = float64(report.Correct) / float64(report.Labelled)
	}
	if report.Tagged > 0 {
		report.TagRecall = tagTotal / float64(report.Tagged)
	}
	return report
}

// Passes is the gate, not a score: a run with a failed call has a hole in it,
// and a hole in a scoring run is not a pass even when the arithmetic survives.
func (r Report) Passes() bool {
	return r.Labelled > 0 && len(r.Errors) == 0 && r.Accuracy >= Threshold
}

// String renders the run a human can read: the two numbers, the gate, the
// per-kind confusion, and every case that went wrong.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d labelled saves of %d cases", r.Labelled, r.Labelled+r.Unlabelled)
	if r.Unlabelled > 0 {
		fmt.Fprintf(&b, " (%d unlabelled, not scored)", r.Unlabelled)
	}
	b.WriteString("\n")

	verdict := "FAIL"
	if r.Passes() {
		verdict = "pass"
	}
	fmt.Fprintf(&b, "kind accuracy  %.1f%%  (%d/%d)  gate %.0f%%  %s\n",
		r.Accuracy*100, r.Correct, r.Labelled, Threshold*100, verdict)
	if r.Tagged > 0 {
		fmt.Fprintf(&b, "tag recall     %.1f%%  over %d saves with human tags\n",
			r.TagRecall*100, r.Tagged)
	}

	kinds := make([]model.Kind, 0, len(r.Kinds))
	for kind := range r.Kinds {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	if len(kinds) > 0 {
		b.WriteString("\n")
	}
	for _, kind := range kinds {
		row := r.Kinds[kind]
		rate := 0.0
		if row.Labelled > 0 {
			rate = float64(row.Correct) / float64(row.Labelled)
		}
		fmt.Fprintf(&b, "%-12s %2d labelled  %2d correct  %3.0f%%", kind, row.Labelled, row.Correct, rate*100)
		for _, mistaken := range sortedKinds(row.Confused) {
			fmt.Fprintf(&b, "   -> %s %d", mistaken, row.Confused[mistaken])
		}
		b.WriteString("\n")
	}

	wrong := r.mistakes()
	if len(wrong) > 0 {
		b.WriteString("\nwrong kinds:\n")
		for _, row := range wrong {
			fmt.Fprintf(&b, "  %-28s wanted %-8s got %s\n", row.ID, row.Want, row.Got)
		}
	}
	if len(r.Errors) > 0 {
		b.WriteString("\nfailures (counted wrong):\n")
		for _, failure := range r.Errors {
			fmt.Fprintf(&b, "  %s\n", failure)
		}
	}
	return b.String()
}

func (r Report) mistakes() []Row {
	var wrong []Row
	for _, row := range r.Rows {
		if row.Err == nil && row.Got != row.Want {
			wrong = append(wrong, row)
		}
	}
	return wrong
}

func sortedKinds(counts map[model.Kind]int) []model.Kind {
	kinds := make([]model.Kind, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// overlap is |want ∩ got|: how many of the owner's tags the model found. The
// caller divides by the label's size, so a model that tags more specifically
// than a human bothers to is not punished as a strict Jaccard would punish it.
func overlap(want, got []string) float64 {
	if len(want) == 0 {
		return 0
	}
	have := make(map[string]bool, len(got))
	for _, tag := range got {
		have[store.NormaliseTag(tag)] = true
	}
	found := 0
	for _, tag := range want {
		if have[store.NormaliseTag(tag)] {
			found++
		}
	}
	return float64(found)
}

func normaliseTags(tags []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		name := store.NormaliseTag(tag)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// --- the set on disk --------------------------------------------------------

// caseFile is the committed shape of one capture. It is deliberately flat and
// hand-editable: a page can be corrected without touching the loader.
type caseFile struct {
	URL      string `json:"url"`
	Captured string `json:"captured"`
	Title    string `json:"title"`
	SiteName string `json:"siteName"`
	Excerpt  string `json:"excerpt"`
	Text     string `json:"text"`
}

// label is one entry in labels.json.
type label struct {
	Kind model.Kind `json:"kind"`
	Tags []string   `json:"tags"`
}

// Load reads a scored set: one <id>.json per capture, plus an optional
// labels.json. Failure here is a broken fixture, not a model result, so every
// complaint names the case.
func Load(dir string) ([]Case, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("enrichscore: reading %s: %w", dir, err)
	}

	labels, err := loadLabels(dir)
	if err != nil {
		return nil, err
	}

	cases := make([]Case, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || name == labelsFile {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("enrichscore: reading %s: %w", name, err)
		}
		var file caseFile
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("enrichscore: %s is not valid JSON: %w", name, err)
		}
		c := Case{
			ID:       id,
			URL:      strings.TrimSpace(file.URL),
			Captured: strings.TrimSpace(file.Captured),
			Title:    file.Title,
			SiteName: file.SiteName,
			Excerpt:  file.Excerpt,
			Text:     file.Text,
		}
		if c.URL == "" {
			return nil, fmt.Errorf("enrichscore: %s has no url", name)
		}
		if strings.TrimSpace(c.Text) == "" {
			return nil, fmt.Errorf("enrichscore: %s captured no page text; re-record it", name)
		}
		if l, ok := labels[id]; ok {
			// A blank kind is a case the owner has not labelled yet; -record
			// writes those so the ids are all in one file to fill in.
			c.Kind = l.Kind
			c.Tags = normaliseTags(l.Tags)
		}
		cases = append(cases, c)
		delete(labels, id)
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("enrichscore: no cases in %s", dir)
	}
	for id := range labels {
		return nil, fmt.Errorf("enrichscore: labels.json names %q, which has no capture", id)
	}

	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	return cases, nil
}

func loadLabels(dir string) (map[string]label, error) {
	raw, err := os.ReadFile(filepath.Join(dir, labelsFile))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]label{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("enrichscore: reading %s: %w", labelsFile, err)
	}
	var labels map[string]label
	if err := json.Unmarshal(raw, &labels); err != nil {
		return nil, fmt.Errorf("enrichscore: %s is not valid JSON: %w", labelsFile, err)
	}
	for id, l := range labels {
		if l.Kind == "" {
			continue // not labelled yet
		}
		if !l.Kind.Valid() {
			return nil, fmt.Errorf("enrichscore: %s labels %q as %q, which is not one of the twelve",
				labelsFile, id, l.Kind)
		}
	}
	return labels, nil
}

// writeCase commits one capture. A re-record is a replacement, so the file is
// truncated rather than appended to.
func writeCase(dir string, c Case) error {
	file := caseFile{
		URL:      c.URL,
		Captured: c.Captured,
		Title:    c.Title,
		SiteName: c.SiteName,
		Excerpt:  c.Excerpt,
		Text:     c.Text,
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("enrichscore: encoding %s: %w", c.ID, err)
	}
	path := filepath.Join(dir, c.ID+".json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("enrichscore: writing %s: %w", path, err)
	}
	return nil
}
