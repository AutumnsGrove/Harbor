package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/export"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

const exportUsage = `harbor export — write the library out as files you own.

Usage: harbor export --out DIR [flags]

Flags:
  --out DIR        the directory to write; it must be new or empty (required)
  --format NAME    all (default), json, markdown, netscape; comma-separated
  --include-media  copy media blobs into media/<sha256>

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
	format := flags.String("format", "all", "all|json|markdown|netscape")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%s\n%s", err, exportUsage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n\n%s", flags.Arg(0), exportUsage)
	}
	if *out == "" {
		return fmt.Errorf("export needs --out DIR\n\n%s", exportUsage)
	}
	formats, err := parseFormats(*format)
	if err != nil {
		return fmt.Errorf("%w\n\n%s", err, exportUsage)
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

	result, err := export.Write(ctx, library, export.Options{
		Dir:           *out,
		Formats:       formats,
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
	fmt.Fprintf(stdout, "  formats: %s\n", strings.Join(names(formats), ", "))
	if *includeMedia {
		fmt.Fprintf(stdout, "  media included\n")
	} else {
		fmt.Fprintf(stdout, "  media not included (--include-media copies the blobs)\n")
	}
	return nil
}

// parseFormats turns --format into the set to write. "all" and an empty value
// mean every format: the readable ones and the restorable one describe the same
// library, and shipping half of them is a half answer.
func parseFormats(value string) ([]export.Format, error) {
	if strings.TrimSpace(value) == "" || value == string(export.FormatAll) {
		return nil, nil
	}
	var formats []export.Format
	for _, part := range strings.Split(value, ",") {
		switch strings.TrimSpace(part) {
		case "":
			continue
		case string(export.FormatJSON):
			formats = append(formats, export.FormatJSON)
		case string(export.FormatMarkdown), "md":
			formats = append(formats, export.FormatMarkdown)
		case string(export.FormatNetscape), "html":
			formats = append(formats, export.FormatNetscape)
		default:
			return nil, fmt.Errorf("export: --format %s is not one of all, json, markdown, netscape", part)
		}
	}
	if len(formats) == 0 {
		return nil, fmt.Errorf("export: --format names nothing")
	}
	return formats, nil
}

func names(formats []export.Format) []string {
	if len(formats) == 0 {
		return []string{"json", "markdown", "netscape"}
	}
	out := make([]string, len(formats))
	for i, format := range formats {
		out[i] = string(format)
	}
	return out
}
