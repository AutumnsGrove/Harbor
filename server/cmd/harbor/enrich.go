package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

const enrichUsage = `harbor enrich — queue the items a bulk import left unread by the model.

Usage: harbor enrich [flags]

Import writes items that are usable immediately (--no-enrich, the documented
default for a big file), which leaves them with no summary, no AI tags and the
default kind. This queues every one of those — state ready, never enriched — for
the enrichment loop the running server already owns. It does not run the model
itself: queueing is instant and safe, the drain is the thing to watch.

Flags:
  --limit N     queue at most N items, oldest saved first (default: all)
  --dry-run     report what would be queued and write nothing

The queue is drained by ` + "`harbor serve`" + `, so the container must be running with
OPENROUTER_API_KEY set; without a key the loop only marks rows ready and nothing
is enriched. Imported items are queued at priority -1, so anything saved by hand
is still read first.
`

// enrichPriority is the bulk backlog's priority: below an interactive save, the
// same value the import itself writes (design.md §5).
const enrichPriority = -1

func enrichCommand(ctx context.Context, getenv func(string) string, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("enrich", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	limit := flags.Int("limit", 0, "queue at most N items (0: all)")
	dryRun := flags.Bool("dry-run", false, "report and write nothing")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%s\n\n%s", err, enrichUsage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("enrich takes no arguments (unexpected %q)\n\n%s", flags.Arg(0), enrichUsage)
	}

	cfg, err := build.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration is not usable:\n%w", err)
	}
	// Queueing without a key would be worse than useless: the loop would mark
	// every row ready in a second and the library would look enriched with
	// nothing in it. Say so instead of writing rows on a promise.
	if cfg.OpenRouterAPIKey == "" {
		return fmt.Errorf("enrichment needs %s: without it the loop only marks items ready and nothing is read by the model", build.EnvOpenRouterAPIKey)
	}

	library, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer library.Close()

	waiting, err := library.UnenrichedCount(ctx)
	if err != nil {
		return err
	}
	if waiting == 0 {
		fmt.Fprintln(stdout, "no items are waiting for their first enrichment")
		return nil
	}

	if *dryRun {
		// The dry run answers the same question the real run does, so it has to
		// honour --limit: `--dry-run --limit 50` is how a batch gets checked
		// before it is paid for.
		would := waiting
		if *limit > 0 && *limit < would {
			would = *limit
		}
		fmt.Fprintf(stdout, "would queue %d item(s) for enrichment at priority %d\n", would, enrichPriority)
		if would < waiting {
			fmt.Fprintf(stdout, "  %d waiting in total; run again for the next batch\n", waiting)
		}
		fmt.Fprintln(stdout, "nothing was written")
		return nil
	}

	queued, remaining, err := library.QueueUnenriched(ctx, enrichPriority, *limit)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "queued %d item(s) for enrichment\n", queued)
	if remaining > 0 {
		fmt.Fprintf(stdout, "  %d still waiting; run this again for the next batch\n", remaining)
	}
	fmt.Fprintln(stdout, "the running server's loop drains them in the background, behind anything saved by hand")
	return nil
}
