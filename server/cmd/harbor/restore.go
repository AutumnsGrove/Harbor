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

	preserved, err := preserveCurrent(ctx, liveSnapshotter(cfg.DataDir), backupsDir, dbPath, time.Now)
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

// snapshotter copies the live database to dest. It is a parameter so the
// fallback below can be tested: a database that opens and then refuses VACUUM
// INTO is the common corruption case, and there is no honest way to build one on
// demand.
type snapshotter func(ctx context.Context, dest string) error

// liveSnapshotter is the production path: open the library, VACUUM INTO dest.
func liveSnapshotter(dataDir string) snapshotter {
	return func(ctx context.Context, dest string) error {
		library, err := store.Open(dataDir)
		if err != nil {
			return err
		}
		defer library.Close()
		return library.Snapshot(ctx, dest)
	}
}

// preserveCurrent makes the current database recoverable before it is replaced.
//
// The fallback is not "the database is corrupt", it is "the snapshot did not
// happen". A partially corrupt database usually still *opens* — VACUUM INTO is
// what fails — so keying the fallback on Open would fail exactly the case it is
// for. Whatever happens, the previous bytes are kept, journals included: a hot
// journal can hold the only copy of the last transaction.
func preserveCurrent(ctx context.Context, take snapshotter, backupsDir, dbPath string, now func() time.Time) (string, error) {
	stamp := now().UTC().Format("20060102T150405Z")
	_, statErr := os.Stat(dbPath)
	haveDB := statErr == nil

	if haveDB {
		if dest, ok := snapshotCurrent(ctx, take, backupsDir, dbPath, stamp); ok {
			return dest, nil
		}
	}
	return moveAside(backupsDir, dbPath, stamp, haveDB)
}

// snapshotCurrent returns the snapshot path, or false when the snapshot did not
// happen for any reason.
func snapshotCurrent(ctx context.Context, take snapshotter, backupsDir, dbPath, stamp string) (string, bool) {
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		return "", false
	}
	dest := filepath.Join(backupsDir, "pre-restore-"+stamp+".db")
	if err := take(ctx, dest); err != nil {
		_ = os.Remove(dest)
		return "", false
	}
	// The snapshot holds whatever the journals held, because VACUUM INTO reads
	// the recovered database. Leaving one would let SQLite replay an old journal
	// into the new file, so a journal that cannot be removed is not a usable
	// preservation: drop the snapshot and fall back, which moves the journals
	// with the database (and fails loudly if that cannot work either).
	if err := removeJournals(dbPath); err != nil {
		_ = os.Remove(dest)
		return "", false
	}
	return dest, true
}

// moveAside keeps an unopenable (or unsnapshotable) database and its journals.
func moveAside(backupsDir, dbPath, stamp string, haveDB bool) (string, error) {
	if err := os.MkdirAll(backupsDir, 0o755); err != nil {
		return "", fmt.Errorf("restore: creating %s: %w", backupsDir, err)
	}
	aside := filepath.Join(backupsDir, "pre-restore-"+stamp+".corrupt")
	if err := os.MkdirAll(aside, 0o755); err != nil {
		return "", fmt.Errorf("restore: creating %s: %w", aside, err)
	}
	if haveDB {
		if err := os.Rename(dbPath, filepath.Join(aside, store.DBFile)); err != nil {
			return "", fmt.Errorf("restore: moving the unopenable database aside: %w", err)
		}
	}
	for _, journal := range journalsOf(dbPath) {
		if err := os.Rename(journal, filepath.Join(aside, filepath.Base(journal))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("restore: moving %s aside: %w", journal, err)
		}
	}
	return aside, nil
}

// journalsOf lists every file SQLite may keep beside the database. A rollback
// journal is as real as a WAL: both can hold the last committed transaction.
func journalsOf(dbPath string) []string {
	return []string{dbPath + "-journal", dbPath + "-wal", dbPath + "-shm"}
}

func removeJournals(dbPath string) error {
	for _, journal := range journalsOf(dbPath) {
		if err := os.Remove(journal); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restore: removing %s: %w", journal, err)
		}
	}
	return nil
}

// replayPoint is the moment the backup was taken: everything the Worker queued
// after it is missing from the restored library and comes back with replay. A
// file that is not named like a backup still restores, so this returns advice
// instead of slicing a name it has not checked.
func replayPoint(backupPath string) string {
	if at, ok := backup.DateOf(backupPath); ok {
		return at.UTC().Format(time.RFC3339)
	}
	return "(unknown: replay from the backup's own date)"
}
