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

--refresh is a different selection for a different reason: it re-queues finished
rows whose stored page image is not an absolute URL (a root-relative or
protocol-relative og:image saved before the pipeline resolved it). Those rows
have already been read by the model, and no other path will ever touch them, so
this is the one deliberate re-read. Missing images are left alone: this fixes bad
values, not absent ones (issue #67).

Read the dry-run number as "stored values that are not absolute", which is not
the same as "covers that fail today": the API resolves a relative image against
the item's URL when it serves it, so most of these render. The ones that are
actually broken came from a page that redirected. Two consequences: the number
overstates the damage, and a row whose re-enrichment cannot change og_json — a
walled video filed from public metadata — keeps matching, so run this once per
deliberate pass rather than expecting the count to reach zero.

Flags:
  --limit N     queue at most N items, oldest saved first (default: all)
  --refresh     re-queue finished items whose stored cover is not an absolute URL
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
	refresh := flags.Bool("refresh", false, "re-queue finished items whose stored cover is not an absolute URL")
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
	if *refresh {
		waiting, err = library.RelativeCoverCount(ctx)
	}
	if err != nil {
		return err
	}
	if waiting == 0 {
		if *refresh {
			fmt.Fprintln(stdout, "no finished item holds a cover that is not an absolute URL")
			return nil
		}
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
		if *refresh {
			fmt.Fprintf(stdout, "would re-queue %d item(s) with a relative cover at priority %d\n", would, enrichPriority)
		} else {
			fmt.Fprintf(stdout, "would queue %d item(s) for enrichment at priority %d\n", would, enrichPriority)
		}
		if would < waiting {
			fmt.Fprintf(stdout, "  %d waiting in total; run again for the next batch\n", waiting)
		}
		fmt.Fprintln(stdout, "nothing was written")
		return nil
	}

	// Exactly one selection. Calling both would spend --limit twice and, with
	// --refresh, quietly queue the whole never-enriched backlog as well — the
	// bill the dry run was supposed to preview (pi-review on #68).
	var queued, remaining int
	if *refresh {
		queued, remaining, err = library.QueueRelativeCovers(ctx, enrichPriority, *limit)
	} else {
		queued, remaining, err = library.QueueUnenriched(ctx, enrichPriority, *limit)
	}
	if err != nil {
		return err
	}
	if *refresh {
		fmt.Fprintf(stdout, "re-queued %d item(s) with a relative cover\n", queued)
	} else {
		fmt.Fprintf(stdout, "queued %d item(s) for enrichment\n", queued)
	}
	if remaining > 0 {
		fmt.Fprintf(stdout, "  %d still waiting; run this again for the next batch\n", remaining)
	}
	fmt.Fprintln(stdout, "the running server's loop drains them in the background, behind anything saved by hand")
	return nil
}
