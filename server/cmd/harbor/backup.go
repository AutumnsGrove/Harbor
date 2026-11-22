package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/backup"
	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
)

const backupUsage = `harbor backup — snapshot, compress and keep the database.

Usage:
  harbor backup [--keep N] [--upload]
                             take a backup now, prune the old ones, and with
                             --upload send the new one through the Worker
  harbor backup --list       list the backups, newest first
  harbor backup --verify     decompress and integrity-check every backup

Backups live in data/backups as harbor-<UTC>.db.zst. The last 30 are kept, and
one that cannot be verified is never deleted: a broken old backup may be the only
copy that opens, and the operator should find out rather than lose it.
`

// backupCommand is the nightly job and the operator's tool (plans/12a).
func backupCommand(ctx context.Context, getenv func(string) string, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	list := flags.Bool("list", false, "list the backups")
	verify := flags.Bool("verify", false, "verify every backup")
	upload := flags.Bool("upload", false, "upload the new backup through the Worker")
	keep := flags.Int("keep", backup.DefaultKeep, "how many backups to keep")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%s\n%s", err, backupUsage)
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q\n\n%s", flags.Arg(0), backupUsage)
	}
	if *list && *verify {
		return fmt.Errorf("--list and --verify are separate runs\n\n%s", backupUsage)
	}
	if *upload && (*list || *verify) {
		return fmt.Errorf("--upload takes a backup, so it cannot be combined with --list or --verify\n\n%s", backupUsage)
	}

	cfg, err := build.Load(getenv)
	if err != nil {
		return fmt.Errorf("configuration is not usable:\n%w", err)
	}
	dir := filepath.Join(cfg.DataDir, "backups")

	switch {
	case *list:
		entries, err := backup.List(dir)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Fprintf(stdout, "no backups in %s\n", dir)
			return nil
		}
		for _, entry := range entries {
			fmt.Fprintf(stdout, "%s  %8s  %s\n",
				entry.Date.UTC().Format(time.RFC3339), humanSize(entry.Bytes), entry.Name)
		}
		return nil

	case *verify:
		checks, err := backup.VerifyAll(dir)
		if err != nil {
			return err
		}
		if len(checks) == 0 {
			fmt.Fprintf(stdout, "no backups in %s\n", dir)
			return nil
		}
		failed := 0
		for _, check := range checks {
			if check.Err != nil {
				failed++
				fmt.Fprintf(stdout, "%s  %8s  FAILED: %v\n",
					check.Entry.Date.UTC().Format(time.RFC3339), humanSize(check.Entry.Bytes), check.Err)
				continue
			}
			fmt.Fprintf(stdout, "%s  %8s  ok\n",
				check.Entry.Date.UTC().Format(time.RFC3339), humanSize(check.Entry.Bytes))
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d backups did not verify", failed, len(checks))
		}
		return nil
	}

	library, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer library.Close()

	// Prune inside Create, so a run either keeps a month of good backups or says
	// why it could not.
	schema, err := library.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	result, err := backup.Create(ctx, library, dir, time.Now, *keep)
	if err != nil {
		return err
	}
	if len(result.Kept) == 0 {
		return fmt.Errorf("backup: the run wrote nothing into %s", dir)
	}
	newest := result.Kept[0]
	fmt.Fprintf(stdout, "backed up %s into %s\n", humanSize(newest.Bytes), newest.Path)
	fmt.Fprintf(stdout, "  %d backup(s) kept\n", len(result.Kept))

	if *upload {
		if err := uploadBackup(ctx, cfg, result, schema, stdout); err != nil {
			return err
		}
	}

	// A backup that failed verification is kept and reported: it may be the only
	// copy that opens, so deleting it is wrong, and staying quiet about it is
	// worse. The run fails, because the timer's log is the only place anyone
	// will look.
	for _, failure := range result.Failed {
		fmt.Fprintf(stdout, "  warning: %s did not verify and was kept: %v\n", failure.Entry.Name, failure.Err)
	}
	if len(result.Failed) > 0 {
		return fmt.Errorf("%d backup(s) failed verification and were kept", len(result.Failed))
	}
	return nil
}

// uploadBackup sends the new snapshot through the Worker — the potato holds no R2
// credential (design.md §4) — and removes the R2 copies of what was just pruned,
// so both sides keep the same thirty.
func uploadBackup(ctx context.Context, cfg build.Config, result backup.Result, schema int, stdout io.Writer) error {
	if cfg.WorkerURL == "" || cfg.WorkerToken == "" {
		return fmt.Errorf("backup --upload needs %s and %s", build.EnvWorkerURL, build.EnvWorkerToken)
	}
	client, err := workerclient.New(cfg.WorkerURL, cfg.WorkerToken, build.Version())
	if err != nil {
		return err
	}
	newest := result.Kept[0]
	if err := putSnapshot(ctx, client, newest, schema); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "  uploaded %s through the Worker\n", newest.Name)

	for _, pruned := range result.Deleted {
		if err := client.DeleteBackup(ctx, pruned.Name); err != nil {
			// The local copy is already gone and the remote one is not: report it
			// rather than pretend the two sides agree.
			return fmt.Errorf("backup: removing %s from R2: %w", pruned.Name, err)
		}
	}
	return nil
}

// putSnapshot streams one file: seekable so the client can retry, hashed first so
// the Worker records what it actually holds.
func putSnapshot(ctx context.Context, client *workerclient.Client, entry backup.Entry, schema int) error {
	file, err := os.Open(entry.Path)
	if err != nil {
		return fmt.Errorf("backup: opening %s: %w", entry.Path, err)
	}
	defer file.Close()

	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return fmt.Errorf("backup: hashing %s: %w", entry.Path, err)
	}
	if size != entry.Bytes {
		return fmt.Errorf("backup: %s is %d bytes, expected %d", entry.Name, size, entry.Bytes)
	}
	return client.PutBackup(ctx, entry.Name, file, size, hex.EncodeToString(hash.Sum(nil)), schema)
}

func humanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	value := float64(bytes)
	for _, suffix := range []string{"KiB", "MiB", "GiB"} {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f%s", value, suffix)
		}
	}
	return fmt.Sprintf("%.1fTiB", value/unit)
}
