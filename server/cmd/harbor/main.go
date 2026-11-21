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
	"syscall"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/enrich"
	enrichnova "github.com/AutumnsGrove/Harbor/server/internal/enrich/nova"
	"github.com/AutumnsGrove/Harbor/server/internal/fetch"
	"github.com/AutumnsGrove/Harbor/server/internal/httpapi"
	"github.com/AutumnsGrove/Harbor/server/internal/queue"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
	"github.com/AutumnsGrove/Harbor/web"
)

const usage = `harbor — one binary, one process.

Usage: harbor <command>

Commands:
  serve         run the HTTP server (the container's process)
  healthcheck   exit 0 when http://127.0.0.1:$HARBOR_PORT/healthz answers 200
  snapshot      write data/backups/pre-update.db (use --pre-update)
  replay        re-ingest the Worker's done jobs (--since 1h|<unix>|<RFC3339>)
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
	case "replay":
		return replayCommand(ctx, getenv, args[1:], stdout)
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
	novaClient := enrichnova.New(enrichnova.Config{APIKey: cfg.OpenRouterAPIKey, Log: log})
	pipeline := enrich.New(
		enrich.HTTPFetcher{Client: fetch.New(fetch.WithUserAgent("Harbor/" + build.Version() + " (+https://github.com/AutumnsGrove/Harbor)"))},
		novaClient, novaClient, novaClient, library, log,
	)
	pipeline.PromptVersion = enrichnova.PromptVersion
	pipeline.EmbeddingModel = enrichnova.EmbedModel
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
		Handler:           httpapi.New(cfg, log, web.Assets(), httpapi.WithStore(library), httpapi.WithPending(pending)).Handler(),
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
