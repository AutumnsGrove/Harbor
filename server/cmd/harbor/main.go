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
	"github.com/AutumnsGrove/Harbor/server/internal/httpapi"
	"github.com/AutumnsGrove/Harbor/web"
)

const usage = `harbor — one binary, one process.

Usage: harbor <command>

Commands:
  serve         run the HTTP server (the container's process)
  healthcheck   exit 0 when http://127.0.0.1:$HARBOR_PORT/healthz answers 200
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

// serve is the container's whole life: load the environment contract, build the
// handler, listen, and stop cleanly when the orchestrator asks.
func serve(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	cfg, err := build.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration is not usable:\n%w", err)
	}

	log := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           httpapi.New(cfg, log, web.Assets()).Handler(),
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
