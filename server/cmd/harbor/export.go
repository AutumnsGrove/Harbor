package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/export"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

const exportUsage = `harbor export — write the library out as files you own.

Usage: harbor export --out DIR [flags]

Flags:
  --out DIR        the directory to write; it must be new or empty (required)
  --include-media  copy media blobs into media/<sha256>
  --format NAME    json (default); markdown and bookmarks follow

An export never needs the network: no Worker, no R2, no model. It reads the
database and, with --include-media, the media directory.
`

// exportCommand writes the library out (plans/12a). It is the invariant that
// outlives every phase: from any shipped version, with nothing else running.
func exportCommand(ctx context.Context, getenv func(string) string, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	out := flags.String("out", "", "the directory to write")
	includeMedia := flags.Bool("include-media", false, "copy media blobs")
	format := flags.String("format", "json", "json")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%s\n%s", err, exportUsage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n\n%s", flags.Arg(0), exportUsage)
	}
	if *out == "" {
		return fmt.Errorf("export needs --out DIR\n\n%s", exportUsage)
	}
	if *format != "json" {
		return fmt.Errorf("export: --format %s does not exist yet; json is the restorable copy\n\n%s", *format, exportUsage)
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

	schemaVersion, err := library.SchemaVersion(ctx)
	if err != nil {
		return err
	}

	result, err := export.JSON(ctx, library, export.Options{
		Dir:           *out,
		IncludeMedia:  *includeMedia,
		MediaDir:      filepath.Join(cfg.DataDir, "media"),
		AppVersion:    build.Version(),
		SchemaVersion: schemaVersion,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "exported the library into %s\n", result.Dir)
	fmt.Fprintf(stdout, "  %d files, schema version %d\n", len(result.Files), schemaVersion)
	if *includeMedia {
		fmt.Fprintf(stdout, "  media included\n")
	} else {
		fmt.Fprintf(stdout, "  media not included (--include-media copies the blobs)\n")
	}
	return nil
}
