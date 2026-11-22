package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/backup"
	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/datalock"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

const restoreUsage = `harbor restore — replace the database from a backup.

Usage: harbor restore <backup.db.zst> [--dry-run] [--yes]

  --dry-run  say what would happen, and change nothing
  --yes      do it

A restore refuses to run while another Harbor holds the data directory, verifies
the backup before touching anything, preserves the current database first (as a
snapshot when it opens, as a moved-aside file when it does not), and finishes by
telling you the replay command to run next.
`

// restoreCommand is the one destructive command in the binary, so it is built
// out of refusals: no lock, no restore; no --yes, no restore; a backup that does
// not verify, no restore; a current database it cannot preserve, no restore.
func restoreCommand(ctx context.Context, getenv func(string) string, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "say what would happen without changing anything")
	confirmed := flags.Bool("yes", false, "run for real")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%s\n%s", err, restoreUsage)
	}
	// The flag package stops at the first positional argument, and `restore
	// backup.db.zst --yes` is how a person writes it. Both booleans are simple,
	// so they are accepted in either position rather than failing with a
	// confusing "needs one backup file".
	var positional []string
	for _, arg := range flags.Args() {
		switch arg {
		case "--yes":
			*confirmed = true
		case "--dry-run":
			*dryRun = true
		default:
			positional = append(positional, arg)
		}
	}
	if len(positional) != 1 {
		return fmt.Errorf("restore needs one backup file\n\n%s", restoreUsage)
	}
	backupPath := positional[0]

	cfg, err := build.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration is not usable:\n%w", err)
	}

	info, err := os.Stat(backupPath)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	dbPath := filepath.Join(cfg.DataDir, store.DBFile)
	backupsDir := filepath.Join(cfg.DataDir, "backups")

	// The lock is what makes "the database is in use" a fact rather than a
	// guess. serve holds it for its whole life.
	held, err := datalock.Acquire(cfg.DataDir)
	if err != nil {
		if errors.Is(err, datalock.ErrHeld) {
			return fmt.Errorf("restore: the database is in use; stop the container first (%w)", err)
		}
		return err
	}
	defer func() { _ = held.Release() }()

	// Verify before touching anything: a backup that cannot be restored must
	// leave the current library exactly as it was.
	if err := backup.Verify(backupPath); err != nil {
		return fmt.Errorf("restore: %w", err)
	}

	fmt.Fprintf(stdout, "restore plan\n")
	fmt.Fprintf(stdout, "  from:  %s (%s)\n", backupPath, humanSize(info.Size()))
	fmt.Fprintf(stdout, "  into:  %s\n", dbPath)
	if current, err := os.Stat(dbPath); err == nil {
		fmt.Fprintf(stdout, "  keeps: the current database first, as a snapshot in %s\n", backupsDir)
		fmt.Fprintf(stdout, "  now:   %s (%s)\n", dbPath, humanSize(current.Size()))
	} else {
		fmt.Fprintf(stdout, "  now:   no database yet\n")
	}

	if *dryRun {
		fmt.Fprintf(stdout, "\ndry run: nothing changed\n")
		return nil
	}
	if !*confirmed {
		return fmt.Errorf("restore: refusing to overwrite %s without --yes", dbPath)
	}

	preserved, err := preserveCurrent(ctx, cfg.DataDir, backupsDir, dbPath, time.Now)
	if err != nil {
		return err
	}
	if preserved != "" {
		fmt.Fprintf(stdout, "kept the current database at %s\n", preserved)
	}

	// Decompress beside the database and rename over it, so a killed process
	// never leaves half a library in place.
	staged := filepath.Join(cfg.DataDir, ".restore-"+time.Now().UTC().Format("20060102T150405Z")+".db")
	defer func() { _ = os.Remove(staged) }()
	if err := backup.Extract(backupPath, staged); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if err := store.Verify(staged); err != nil {
		return fmt.Errorf("restore: the extracted database does not verify: %w", err)
	}
	// A WAL or SHM file belonging to the old database must not sit beside the new
	// one; preserveCurrent has already dealt with them.
	if err := os.Rename(staged, dbPath); err != nil {
		return fmt.Errorf("restore: replacing %s: %w", dbPath, err)
	}

	fmt.Fprintf(stdout, "restored %s into %s\n", backupPath, dbPath)
	fmt.Fprintf(stdout, "next: harbor replay --since %s\n", replayPoint(backupPath))
	return nil
}

// preserveCurrent makes the current database recoverable before it is replaced.
// A database that opens is snapshotted (consistent, WAL included); one that does
// not is moved aside with its journals, because a restore is exactly when you
// have a corrupt database and still must not throw the bytes away.
func preserveCurrent(ctx context.Context, dataDir, backupsDir, dbPath string, now func() time.Time) (string, error) {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		return "", fmt.Errorf("restore: creating %s: %w", backupsDir, err)
	}
	stamp := now().UTC().Format("20060102T150405Z")

	library, err := store.Open(dataDir)
	if err == nil {
		defer library.Close()
		dest := filepath.Join(backupsDir, "pre-restore-"+stamp+".db")
		if err := library.Snapshot(ctx, dest); err != nil {
			return "", fmt.Errorf("restore: snapshotting the current database: %w", err)
		}
		// The snapshot holds the WAL's contents, so the journals can go.
		for _, journal := range []string{dbPath + "-wal", dbPath + "-shm"} {
			if err := os.Remove(journal); err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("restore: removing %s: %w", journal, err)
			}
		}
		return dest, nil
	}

	aside := filepath.Join(backupsDir, "pre-restore-"+stamp+".corrupt")
	if err := os.MkdirAll(aside, 0o755); err != nil {
		return "", fmt.Errorf("restore: creating %s: %w", aside, err)
	}
	moved := filepath.Join(aside, store.DBFile)
	if err := os.Rename(dbPath, moved); err != nil {
		return "", fmt.Errorf("restore: moving the unopenable database aside: %w", err)
	}
	for _, journal := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Rename(journal, filepath.Join(aside, filepath.Base(journal))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("restore: moving %s aside: %w", journal, err)
		}
	}
	return aside, nil
}

// replayPoint is the moment the backup was taken: everything the Worker queued
// after it is missing from the restored library and comes back with replay.
func replayPoint(backupPath string) string {
	name := filepath.Base(backupPath)
	trimmed := name[len("harbor-"):]
	trimmed = trimmed[:len(trimmed)-len(".db.zst")]
	if at, err := time.Parse("20060102T150405Z", trimmed); err == nil {
		return at.UTC().Format(time.RFC3339)
	}
	return "(unknown: replay from the backup's date)"
}
