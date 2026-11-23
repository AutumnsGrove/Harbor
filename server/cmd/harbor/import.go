package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/importer"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

const importUsage = `harbor import — move an old bookmark library in.

Usage: harbor import FILE [flags]

The file is a Raindrop CSV export (the format Raindrop's Settings → Export
writes) or a Netscape bookmarks.html. It is detected from the contents, so the
name does not matter.

Flags:
  --dry-run     report what would happen and write nothing (run this first)
  --no-enrich   write items that are usable immediately instead of queueing
                them for the model; the documented choice for a big file
  --limit N     stop after N rows
  --report FILE write the report as JSON to FILE

Import is idempotent: running it twice leaves the same library, so a cancelled
or interrupted run is safe to repeat. Take a snapshot first (` + "`harbor backup`" + `, and see
docs/runbook.md) — this is the operation that doubles a library if it is wrong,
and the snapshot is the way back.
`

// importCommand is the real interface for a big file: the UI's drop zone is
// convenience, the CLI is what runs against the export that matters (plans/05).
func importCommand(ctx context.Context, getenv func(string) string, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "report and write nothing")
	noEnrich := flags.Bool("no-enrich", false, "write usable items instead of queueing them")
	limit := flags.Int("limit", 0, "stop after N rows")
	reportPath := flags.String("report", "", "write the JSON report to this file")
	// Go's flag package stops at the first positional argument, and people write
	// `import FILE --dry-run` far more often than the other way round, so the
	// file is taken off the front when it is there.
	var path string
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		path, rest = args[0], args[1:]
	}
	if err := flags.Parse(rest); err != nil {
		return fmt.Errorf("%s\n\n%s", err, importUsage)
	}
	if path == "" {
		if flags.NArg() != 1 {
			return fmt.Errorf("import takes exactly one file\n\n%s", importUsage)
		}
		path = flags.Arg(0)
	} else if flags.NArg() > 0 {
		return fmt.Errorf("import takes exactly one file (unexpected %q)\n\n%s", flags.Arg(0), importUsage)
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer file.Close()

	sum, err := fileSHA256(file)
	if err != nil {
		return err
	}
	source, err := importer.NewSource(file)
	if err != nil {
		return fmt.Errorf("%s is not a bookmark file: %w", filepath.Base(path), err)
	}

	cfg, err := build.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration is not usable:\n%w", err)
	}
	library, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer library.Close()

	// A dry run writes nothing, import_runs included: it reports what the rest
	// of the file would do and leaves no trace.
	if *dryRun {
		report, err := importer.Run(ctx, library, source, importer.Options{DryRun: true, Limit: *limit})
		if err := writeImportReport(stdout, report, true); err != nil {
			return err
		}
		if *reportPath != "" {
			if err := writeImportReportJSON(*reportPath, report); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "report written to %s\n", *reportPath)
		}
		return err
	}

	report, tracked, err := importer.RunTracked(ctx, library, sum, source, importer.Options{
		NoEnrich:        *noEnrich,
		Limit:           *limit,
		CheckpointEvery: 100,
		OnStart: func(resumed bool, from int) {
			if resumed {
				fmt.Fprintf(stdout, "resuming the cancelled run of this file from record %d\n", from)
			}
		},
	})

	// A report is printed even when the run stopped early: the counts are how a
	// human finds out how far it got.
	if writeErr := writeImportReport(stdout, report, false); writeErr != nil {
		return writeErr
	}
	if err != nil {
		return err
	}

	// A Ctrl-C is a controlled stop, not a failure: RunTracked has already
	// closed the run as cancelled, so the same command resumes it.
	if tracked.Stopped {
		fmt.Fprintf(stdout, "stopped at record %d; run the same command again to resume\n", tracked.From+report.Total)
	}

	if *reportPath != "" {
		if err := writeImportReportJSON(*reportPath, report); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "report written to %s\n", *reportPath)
	}
	return err
}

// writeImportReport is the human summary. The dry run says so, because the
// numbers mean something different when nothing was written.
func writeImportReport(stdout io.Writer, report importer.Report, dryRun bool) error {
	mode := "imported"
	if dryRun {
		mode = "would import"
	}
	fmt.Fprintf(stdout, "%s %d of %d rows\n", mode, report.Imported, report.Total)
	fmt.Fprintf(stdout, "  %d merged into items already in the library\n", report.Merged)
	if report.Skipped > 0 {
		fmt.Fprintf(stdout, "  %d skipped (no URL)\n", report.Skipped)
	}
	if report.Failed > 0 {
		fmt.Fprintf(stdout, "  %d failed\n", report.Failed)
	}
	if dryRun {
		fmt.Fprintf(stdout, "  %d Spaces would be created\n", report.SpacesToCreate)
	} else {
		fmt.Fprintf(stdout, "  %d Spaces created\n", report.SpacesCreated)
	}
	fmt.Fprintf(stdout, "  %d highlights, %d favourites\n", report.Highlights, report.Favourites)
	if len(report.UnknownColumns) > 0 {
		fmt.Fprintf(stdout, "  unknown columns (ignored): %s\n", strings.Join(report.UnknownColumns, ", "))
	}
	for _, warning := range report.FileWarnings {
		fmt.Fprintf(stdout, "  warning: %s\n", warning)
	}
	for _, rowErr := range report.Errors {
		fmt.Fprintf(stdout, "  failed record %d (%s): %s\n", rowErr.Record, rowErr.URL, rowErr.Reason)
	}
	if dryRun {
		fmt.Fprintln(stdout, "nothing was written; run again without --dry-run to import")
	}
	return nil
}

// fileSHA256 is the import run's key: the same file resumes, different content
// starts a fresh run, and the path is irrelevant.
func fileSHA256(file *os.File) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hashing the file: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewinding the file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeImportReportJSON(path string, report importer.Report) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the report: %w", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
