// `harbor replay --since <time>` re-ingests the Worker's done jobs. It is the
// other half of the durability rule (design.md §5): after a rollback or a
// restore, the saves made during the window are pulled again, and the store's
// processed_jobs row makes the overlap a no-op rather than a duplicate.
package main

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/queue"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
)

func replayCommand(ctx context.Context, getenv func(string) string, args []string, stdout io.Writer) error {
	since, err := parseSince(args, time.Now)
	if err != nil {
		return err
	}

	cfg, err := build.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration is not usable:\n%w", err)
	}
	if cfg.WorkerURL == "" || cfg.WorkerToken == "" {
		return fmt.Errorf("replay needs %s and %s: it asks the Worker for its done jobs", build.EnvWorkerURL, build.EnvWorkerToken)
	}

	client, err := workerclient.New(cfg.WorkerURL, cfg.WorkerToken, build.Version())
	if err != nil {
		return err
	}
	library, err := store.Open(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("the library at %s is not usable: %w", cfg.DataDir, err)
	}
	defer func() { _ = library.Close() }()

	jobs, err := client.Replay(ctx, since)
	if err != nil {
		return fmt.Errorf("asking the Worker for jobs since %s: %w", since.Format(time.RFC3339), err)
	}

	ingestor := queue.NewIngestor(client, library, nil)
	stats, err := ingestor.IngestAll(ctx, jobs)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "replay since %s: %d jobs, %d new, %d merged, %d already ingested, %d failed\n",
		since.UTC().Format(time.RFC3339), stats.Claimed, stats.Ingested-stats.Merged, stats.Merged, stats.Already, stats.Failed)
	if stats.Failed > 0 {
		return fmt.Errorf("%d job(s) could not be replayed; see the Worker's pending list", stats.Failed)
	}
	return nil
}

// parseSince reads `--since 1h`, `--since=1h`, a unix timestamp or an RFC3339
// time. It is required: a replay with an accidental default could re-read
// months of jobs, and the Worker only keeps done jobs anyway.
func parseSince(args []string, now func() time.Time) (time.Time, error) {
	var raw string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--since" || arg == "-since":
			if i+1 >= len(args) {
				return time.Time{}, fmt.Errorf("%s needs a value", arg)
			}
			raw = args[i+1]
			i++
		case strings.HasPrefix(arg, "--since="):
			raw = strings.TrimPrefix(arg, "--since=")
		case strings.HasPrefix(arg, "-since="):
			raw = strings.TrimPrefix(arg, "-since=")
		default:
			return time.Time{}, fmt.Errorf("unknown argument %q (usage: harbor replay --since <1h|unix|RFC3339>)", arg)
		}
	}
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, fmt.Errorf("--since is required (a duration such as 1h, a unix timestamp, or an RFC3339 time)")
	}
	return parseWhen(raw, now)
}

// parseWhen accepts the three spellings an operator actually has to hand: what
// they remember ("two hours ago" is easier typed as 2h), what a log line shows
// (RFC3339), and what the API returns (unix seconds).
func parseWhen(raw string, now func() time.Time) (time.Time, error) {
	value := strings.TrimSpace(raw)

	if d, err := time.ParseDuration(value); err == nil {
		// Zero is refused rather than read as "now": an operator typing 0 more
		// likely means the epoch, and silently replaying nothing is worse than
		// asking them to say which they meant.
		if d <= 0 {
			return time.Time{}, fmt.Errorf("--since %q must be a positive duration", raw)
		}
		return now().UTC().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC(), nil
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Unix(seconds, 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("--since %q is not a duration, a unix timestamp or an RFC3339 time", raw)
}
