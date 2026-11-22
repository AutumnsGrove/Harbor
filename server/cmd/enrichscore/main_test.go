package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	"github.com/AutumnsGrove/Harbor/server/internal/enrichscore"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

// The scored run must ask the model exactly what the pipeline asks, or the
// number it prints measures a prompt nobody ships (plans/03, "Scored fixture
// set"). These tests pin the two calls and the failure bookkeeping.

type fakeDecider struct {
	kind   model.Kind
	err    error
	inputs []enrich.ClassifyInput
}

func (f *fakeDecider) Decide(_ context.Context, input enrich.ClassifyInput) (enrich.Classification, error) {
	f.inputs = append(f.inputs, input)
	if f.err != nil {
		return enrich.Classification{}, f.err
	}
	return enrich.Classification{Kind: f.kind}, nil
}

type fakeWriter struct {
	tags   []string
	err    error
	inputs []enrich.ContentInput
}

func (f *fakeWriter) Write(_ context.Context, input enrich.ContentInput) (enrich.Content, error) {
	f.inputs = append(f.inputs, input)
	if f.err != nil {
		return enrich.Content{}, f.err
	}
	return enrich.Content{Title: "T", Summary: "S.", Tags: f.tags}, nil
}

func TestScoreAllAsksBothSurfaces(t *testing.T) {
	decider := &fakeDecider{kind: model.KindRecipe}
	writer := &fakeWriter{tags: []string{"baking", "bread", "breakfast"}}
	cases := []enrichscore.Case{{
		ID: "pancakes", URL: "https://kitchen.example/pancakes", Title: "Pancakes",
		SiteName: "Kitchen", Excerpt: "A use for the discard.",
		Text: strings.Repeat("Whisk. ", 3_000),
		Kind: model.KindRecipe,
	}}

	answers := scoreAll(context.Background(), cases, decider, writer, nil)
	if len(answers) != 1 || answers[0].Err != nil {
		t.Fatalf("answers = %+v", answers)
	}
	if answers[0].Kind != model.KindRecipe || len(answers[0].Tags) != 3 {
		t.Errorf("answer = %+v", answers[0])
	}
	if len(decider.inputs) != 1 || len(writer.inputs) != 1 {
		t.Fatalf("decide/write calls = %d/%d, want 1/1", len(decider.inputs), len(writer.inputs))
	}
	if len(decider.inputs[0].Text) > 4_000 {
		t.Errorf("decide saw %d bytes, more than the pipeline's cap", len(decider.inputs[0].Text))
	}
	if writer.inputs[0].Kind != model.KindRecipe {
		t.Errorf("write kind = %q, want the decided kind", writer.inputs[0].Kind)
	}
	if writer.inputs[0].URL != cases[0].URL {
		t.Errorf("write url = %q", writer.inputs[0].URL)
	}
}

func TestScoreAllPassesTheVocabulary(t *testing.T) {
	// Production sends the library's canonical tags with the write call; a run
	// without them measures a fresh install's prompt (plans/03).
	decider := &fakeDecider{kind: model.KindArticle}
	writer := &fakeWriter{tags: []string{"a", "b", "c"}}
	cases := []enrichscore.Case{{ID: "x", URL: "https://example.test/x", Text: "body", Kind: model.KindArticle}}

	scoreAll(context.Background(), cases, decider, writer, []string{"go", "rust"})
	if len(writer.inputs) != 1 {
		t.Fatalf("write calls = %d", len(writer.inputs))
	}
	if got := writer.inputs[0].Vocabulary; len(got) != 2 || got[0] != "go" || got[1] != "rust" {
		t.Errorf("vocabulary = %v, want the one given", got)
	}
}

func TestScoreAllCoercesAnInventedKind(t *testing.T) {
	decider := &fakeDecider{kind: model.Kind("manifesto")}
	writer := &fakeWriter{tags: []string{"a", "b", "c"}}
	cases := []enrichscore.Case{{ID: "x", URL: "https://example.test/x", Text: "body", Kind: model.KindArticle}}

	answers := scoreAll(context.Background(), cases, decider, writer, nil)
	if answers[0].Kind != model.KindArticle {
		t.Errorf("kind = %q, want article (the pipeline's coercion)", answers[0].Kind)
	}
	if writer.inputs[0].Kind != model.KindArticle {
		t.Errorf("the write call saw %q, want article", writer.inputs[0].Kind)
	}
}

func TestScoreAllKeepsGoingAfterAFailedCall(t *testing.T) {
	decider := &fakeDecider{err: errors.New("nova classify: 429")}
	writer := &fakeWriter{tags: []string{"a", "b", "c"}}
	cases := []enrichscore.Case{
		{ID: "a", URL: "https://example.test/a", Text: "body", Kind: model.KindArticle},
		{ID: "b", URL: "https://example.test/b", Text: "body", Kind: model.KindArticle},
	}
	answers := scoreAll(context.Background(), cases, decider, writer, nil)
	if len(answers) != 2 {
		t.Fatalf("answers = %d, want one per labelled case", len(answers))
	}
	for _, answer := range answers {
		if answer.Err == nil {
			t.Errorf("%s has no error, want the call's failure", answer.ID)
		}
	}
	if len(writer.inputs) != 0 {
		t.Errorf("the write call ran after classify failed %d times", len(writer.inputs))
	}
}

func TestScoreAllSkipsUnlabelledCases(t *testing.T) {
	decider := &fakeDecider{kind: model.KindArticle}
	writer := &fakeWriter{tags: []string{"a", "b", "c"}}
	cases := []enrichscore.Case{
		{ID: "a", URL: "https://example.test/a", Text: "body"}, // captured, not labelled
		{ID: "b", URL: "https://example.test/b", Text: "body", Kind: model.KindArticle},
	}
	answers := scoreAll(context.Background(), cases, decider, writer, nil)
	if len(answers) != 1 || answers[0].ID != "b" {
		t.Fatalf("answers = %+v, want only the labelled case", answers)
	}
	if len(decider.inputs) != 1 {
		t.Errorf("decide calls = %d, want 1: an unlabelled case must not cost a model call", len(decider.inputs))
	}
}
