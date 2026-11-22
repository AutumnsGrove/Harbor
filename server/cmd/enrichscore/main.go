// Command enrichscore measures the enrichment prompt against a set of real
// saves with human labels (plans/03, "Scored fixture set"): `make enrich-score`.
//
// It is a development tool, not part of the shipped binary: it is never built
// into the image, and it needs a key the server does not share with it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	enrichnova "github.com/AutumnsGrove/Harbor/server/internal/enrich/nova"
	"github.com/AutumnsGrove/Harbor/server/internal/enrichscore"
	"github.com/AutumnsGrove/Harbor/server/internal/fetch"
	"github.com/AutumnsGrove/Harbor/server/internal/model"
)

const usage = `enrichscore — measure the enrichment prompt against real saves.

Usage: enrichscore [flags]
       enrichscore -record SOURCES.txt [flags]

Flags:
  -dir DIR      the scored set (default server/internal/enrichscore/testdata/saves)
  -min N        refuse to score fewer than N labelled saves (default 20)
  -record FILE  capture every "id url" in FILE through fetch and extract, then
                add any new id to labels.json with a blank kind

Scoring calls Nova with OPENROUTER_API_KEY, the same key the server uses, and
prints the kind accuracy, the gate, the per-kind confusion and the tag recall.
A set with unlabelled cases is scored on the labelled ones.

Exit status is non-zero when the gate fails, when any call fails, or when
-record could not capture some page.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "enrichscore:", err)
		os.Exit(1)
	}
}

// run is main without the process, so the gate is testable.
func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	flags := flag.NewFlagSet("enrichscore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dir := flags.String("dir", "server/internal/enrichscore/testdata/saves", "the scored set")
	minimum := flags.Int("min", 20, "refuse to score fewer than this many labelled saves")
	record := flags.String("record", "", "capture every `id url` in this file")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%s\n%s", err, usage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n\n%s", flags.Arg(0), usage)
	}

	if *record != "" {
		return recordCommand(ctx, *record, *dir, stdout)
	}
	return scoreCommand(ctx, *dir, *minimum, getenv, stdout)
}

// recordCommand refreshes the captures. It is the only mode that touches the
// network before a model is involved.
func recordCommand(ctx context.Context, sourcesPath, dir string, stdout io.Writer) error {
	file, err := os.Open(sourcesPath)
	if err != nil {
		return err
	}
	defer file.Close()

	sources, err := enrichscore.ParseSources(file)
	if err != nil {
		return err
	}
	fetcher := enrich.HTTPFetcher{Client: fetch.New()}
	failures, err := enrichscore.Record(ctx, fetcher, dir, sources, time.Now)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "recorded %d of %d pages into %s\n", len(sources)-len(failures), len(sources), dir)
	for _, failure := range failures {
		fmt.Fprintf(stdout, "  failed %-28s %v\n", failure.ID, failure.Err)
	}
	if len(failures) > 0 {
		return fmt.Errorf("%d page(s) were not captured", len(failures))
	}
	return nil
}

// scoreCommand asks Nova for every labelled case and prints the report.
func scoreCommand(ctx context.Context, dir string, minimum int, getenv func(string) string, stdout io.Writer) error {
	cases, err := enrichscore.Load(dir)
	if err != nil {
		return err
	}
	labelled := 0
	for _, c := range cases {
		if c.Kind != "" {
			labelled++
		}
	}
	if labelled < minimum {
		return fmt.Errorf("%s has %d labelled saves, -min is %d; fill in labels.json", dir, labelled, minimum)
	}

	key := getenv(build.EnvOpenRouterAPIKey)
	if key == "" {
		return fmt.Errorf("%s is not set; scoring calls Nova", build.EnvOpenRouterAPIKey)
	}
	// The adapter's usage log is the only record of what a run cost.
	client := enrichnova.New(enrichnova.Config{APIKey: key, Log: slog.New(slog.NewTextHandler(os.Stderr, nil))})

	answers := scoreAll(ctx, cases, client, client)
	for i, answer := range answers {
		status := string(answer.Kind)
		if answer.Err != nil {
			status = "FAILED: " + answer.Err.Error()
		}
		fmt.Fprintf(stdout, "%2d/%d %-28s %s\n", i+1, len(answers), answer.ID, status)
	}
	fmt.Fprintln(stdout)

	report := enrichscore.Score(cases, answers)
	fmt.Fprint(stdout, report)
	if !report.Passes() {
		return fmt.Errorf("the scored set did not pass: %d of %d correct", report.Correct, report.Labelled)
	}
	return nil
}

// scoreAll runs the two calls the pipeline runs per item, in the same order and
// with the same caps, and coerces an invented kind to article exactly as
// Process does — a score must measure what production would store.
func scoreAll(ctx context.Context, cases []enrichscore.Case, decider enrich.Decider, writer enrich.Writer) []enrichscore.Answer {
	answers := make([]enrichscore.Answer, 0, len(cases))
	for _, c := range cases {
		if c.Kind == "" {
			continue
		}
		page := enrich.PageText{URL: c.URL, Title: c.Title, SiteName: c.SiteName, Excerpt: c.Excerpt, Text: c.Text}
		answer := enrichscore.Answer{ID: c.ID}
		classification, err := decider.Decide(ctx, enrich.ClassifyFor(page))
		if err != nil {
			answer.Err = err
			answers = append(answers, answer)
			continue
		}
		kind := classification.Kind
		if !kind.Valid() {
			kind = model.KindArticle
		}
		answer.Kind = kind
		content, err := writer.Write(ctx, enrich.ContentFor(page, kind))
		if err != nil {
			answer.Err = err
			answers = append(answers, answer)
			continue
		}
		answer.Tags = content.Tags
		answers = append(answers, answer)
	}
	return answers
}
