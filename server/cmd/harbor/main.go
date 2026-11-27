// Command harbor is the whole product: one process serving the API, the
// embedded SvelteKit build and the CLI subcommands the image and the update
// watcher call (design.md §3).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/cover"
	"github.com/AutumnsGrove/Harbor/server/internal/datalock"
	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	enrichnova "github.com/AutumnsGrove/Harbor/server/internal/enrich/nova"
	"github.com/AutumnsGrove/Harbor/server/internal/extract"
	"github.com/AutumnsGrove/Harbor/server/internal/fetch"
	"github.com/AutumnsGrove/Harbor/server/internal/httpapi"
	"github.com/AutumnsGrove/Harbor/server/internal/queue"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
	"github.com/AutumnsGrove/Harbor/server/internal/youtube"
	"github.com/AutumnsGrove/Harbor/web"
)

const usage = `harbor — one binary, one process.

Usage: harbor <command>

Commands:
  serve         run the HTTP server (the container's process)
  healthcheck   exit 0 when http://127.0.0.1:$HARBOR_PORT/healthz answers 200
  snapshot      write data/backups/pre-update.db (use --pre-update)
  export        write the library out as files (--out DIR [--format ...] [--include-media])
  backup        snapshot, compress and keep the database (--list, --verify)
  restore       replace the database from a backup (--dry-run, --yes)
  replay        re-ingest the Worker's done jobs (--since 1h|<unix>|<RFC3339>)
  import        move a Raindrop CSV or bookmarks.html in (--dry-run, --no-enrich)
  enrich        queue what a bulk import left unread by the model (--limit, --dry-run)
  version       print the build stamp
  help          print this text
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "harbor:", err)
		os.Exit(1)
	}
}

// run is main without the process, so every command is testable.
func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("no command given\n\n%s", usage)
	}
	switch args[0] {
	case "serve":
		return serve(ctx, getenv, stdout)
	case "healthcheck":
		return healthcheck(getenv)
	case "snapshot":
		return snapshotCommand(ctx, getenv, args[1:], stdout)
	case "export":
		return exportCommand(ctx, getenv, args[1:], stdout)
	case "backup":
		return backupCommand(ctx, getenv, args[1:], stdout)
	case "restore":
		return restoreCommand(ctx, getenv, args[1:], stdout)
	case "replay":
		return replayCommand(ctx, getenv, args[1:], stdout)
	case "import":
		return importCommand(ctx, getenv, args[1:], stdout)
	case "enrich":
		return enrichCommand(ctx, getenv, args[1:], stdout)
	case "version":
		fmt.Fprintln(stdout, build.Version())
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

// enrichmentProcessor builds the enrichment stage. With a Nova key it is the
// real pipeline, and its prompt version and embedding model come from the
// adapter's constants so the row records what was actually called; without a
// key it returns nil, which is the phase-2 shell, so a missing key never stops
// a save (design.md §6).
func enrichmentProcessor(cfg build.Config, library *store.Store, log *slog.Logger) queue.Processor {
	if cfg.OpenRouterAPIKey == "" {
		log.Warn("OPENROUTER_API_KEY is not set; enrichment will only mark items ready", "env", build.EnvOpenRouterAPIKey)
		return nil
	}
	// The same place that logs a `nova call` records its cost against the item,
	// so the Settings panel and the log line reconcile (issue #52).
	novaClient := enrichnova.New(enrichnova.Config{APIKey: cfg.OpenRouterAPIKey, Log: log, Usage: library})
	// The fetcher presents a browser User-Agent (design §6, open-questions P1):
	// the build's own name is what 40 of the library's saves were being refused
	// for, and a page that then serves no readable text files as a wall.
	// Documents get their own body cap: 5 MB is right for a page and wrong for
	// a paper, and the PDF reader is the only reason to hold a bigger one
	// (design §6, issue #74).
	pipeline := enrich.New(
		enrich.HTTPFetcher{Client: fetch.New(fetch.WithDocumentCap(extract.IsDocumentBody))},
		novaClient, novaClient, novaClient, library, log,
	)
	pipeline.PromptVersion = enrichnova.PromptVersion
	pipeline.EmbeddingModel = enrichnova.EmbedModel
	// A video's page is the one a datacenter IP cannot read (spike S8: Google's
	// /sorry/), so the public metadata endpoint is the way such a save is still
	// filed with a title, a channel and a still. No key: the endpoint is public.
	pipeline.Videos = youtube.New(youtube.Config{
		UserAgent: "Harbor/" + build.Version() + " (+https://github.com/AutumnsGrove/Harbor)",
	})
	return pipeline
}

// serve is the container's whole life: load the environment contract, build the
// handler, listen, and stop cleanly when the orchestrator asks.
func serve(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	cfg, err := build.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration is not usable:\n%w", err)
	}

	log := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// One owner per data directory. serve holds this for its whole life, so a
	// restore can tell "the database is in use" without guessing from a pidfile
	// (plans/12a).
	held, err := datalock.Acquire(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("another Harbor is already using %s: %w", cfg.DataDir, err)
	}
	defer func() {
		if err := held.Release(); err != nil {
			log.Error("releasing the data lock", "error", err)
		}
	}()

	// The library opens before the listener: a database that cannot be opened is
	// a failed update, and the watcher should roll it back rather than serve an
	// empty library. The healthcheck still answers, because it touches nothing.
	library, err := store.OpenWith(cfg.DataDir, log)
	if err != nil {
		return fmt.Errorf("the library at %s is not usable: %w", cfg.DataDir, err)
	}
	defer func() {
		if err := library.Close(); err != nil {
			log.Error("closing the library", "error", err)
		}
	}()

	// Two goroutines, two queues (design.md §5): ingest pulls the Worker, and
	// enrichment works the item rows. Enrichment runs even with no Worker,
	// because an in-app save enters the same queue.
	workCtx, stopWork := context.WithCancel(context.Background())
	defer stopWork()

	go queue.NewEnricher(library, enrichmentProcessor(cfg, library, log), log).Run(workCtx)

	// Covers are fetched lazily: a card nobody scrolls to costs nothing, and the
	// page fetch has already spent its time (issue #49). A directory that cannot
	// be created is a startup failure, not a silent placeholder for every card.
	covers, err := cover.Open(filepath.Join(cfg.DataDir, "covers"),
		cover.HTTP{Client: fetch.New()}, log)
	if err != nil {
		return err
	}

	// pending is what /api/pending shows: the saves still at the Worker. Nil is
	// a valid state and answers an empty list.
	var pending httpapi.PendingSource
	switch {
	case cfg.WorkerURL == "" && cfg.WorkerToken == "":
		log.Warn("no Worker configured; remote saves will not be pulled", "env", build.EnvWorkerURL)
	case cfg.WorkerURL == "" || cfg.WorkerToken == "":
		return fmt.Errorf("%s and %s must be set together", build.EnvWorkerURL, build.EnvWorkerToken)
	default:
		client, err := workerclient.New(cfg.WorkerURL, cfg.WorkerToken, build.Version())
		if err != nil {
			return err
		}
		pending = client
		go queue.NewLoop(queue.NewIngestor(client, library, log)).Run(workCtx)
	}

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           httpapi.New(cfg, log, web.Assets(), httpapi.WithStore(library), httpapi.WithPending(pending), httpapi.WithCovers(covers)).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}

	listening := make(chan error, 1)
	go func() {
		log.Info("harbor listening",
			"addr", server.Addr,
			"version", build.Version(),
			"deployment", cfg.Deployment,
			"data", cfg.DataDir,
			"update_dir", cfg.UpdateDir,
		)
		listening <- server.ListenAndServe()
	}()

	select {
	case err := <-listening:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		log.Info("shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
