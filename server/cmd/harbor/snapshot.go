package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// snapshotCommand is the updater's first step: a consistent copy of
// harbor.db before the image changes, so a bad image has something to roll
// back to (design.md §3).
//
// The destination is not a parameter. The watcher and phase 12's backups both
// want "the pre-update snapshot", and letting a caller name a path is one more
// way to overwrite the wrong file.
func snapshotCommand(ctx context.Context, getenv func(string) string, args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "--pre-update" {
		return fmt.Errorf("snapshot needs --pre-update\n\n%s", usage)
	}

	cfg, err := build.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration is not usable:\n%w", err)
	}

	opened, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer opened.Close()

	dest := filepath.Join(cfg.DataDir, "backups", "pre-update.db")
	start := time.Now()
	if err := opened.Snapshot(ctx, dest); err != nil {
		return err
	}

	info, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("snapshot was written but cannot be read back: %w", err)
	}
	fmt.Fprintf(stdout, "snapshot: %s (%d bytes, %s)\n", dest, info.Size(), time.Since(start).Round(time.Millisecond))
	return nil
}
