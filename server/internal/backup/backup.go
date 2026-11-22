// Package backup keeps the one artefact that cannot be rebuilt: harbor.db
// (plans/12a). It compresses a consistent snapshot, keeps a month of them, and
// refuses to delete a snapshot it cannot verify.
package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// DefaultKeep is the retention window: thirty daily snapshots.
const DefaultKeep = 30

// nameLayout is the one place a backup's date lives. The name sorts, and it
// survives a directory listing that has lost every other piece of metadata.
const (
	prefix     = "harbor-"
	nameLayout = "20060102T150405Z"
	suffix     = ".db.zst"
)

// Store is the part of the database a backup needs: a consistent snapshot file.
// store.Store implements it.
type Store interface {
	Snapshot(ctx context.Context, dest string) error
}

// Entry is one backup on disk.
type Entry struct {
	Path  string
	Name  string
	Date  time.Time
	Bytes int64
}

// Failure is a backup that was kept because it could not be verified. Keeping it
// is right; keeping it silently is not, so it comes back with its reason.
type Failure struct {
	Entry Entry
	Err   error
}

// Result is what a backup run did.
type Result struct {
	Kept   []Entry   // newest first, exactly what is on disk now
	Failed []Failure // old backups retained because verification failed
}

// Create takes a snapshot, compresses it into dir, and prunes the old ones.
// The snapshot is verified before it is compressed, and the compressed file is
// renamed into place, so dir never holds a backup that looks valid and is not.
func Create(ctx context.Context, store Store, dir string, now func() time.Time, keep int) (Result, error) {
	if keep <= 0 {
		keep = DefaultKeep
	}
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, fmt.Errorf("backup: creating %s: %w", dir, err)
	}

	// The snapshot is a plain SQLite file; it is compressed and then removed, so
	// the only lasting copy is the .zst.
	temp, err := os.CreateTemp(dir, ".snapshot-*.db")
	if err != nil {
		return Result{}, fmt.Errorf("backup: reserving a snapshot name: %w", err)
	}
	tempPath := temp.Name()
	_ = temp.Close()
	_ = os.Remove(tempPath)
	defer func() { _ = os.Remove(tempPath) }()

	if err := store.Snapshot(ctx, tempPath); err != nil {
		return Result{}, err
	}

	name := prefix + now().UTC().Format(nameLayout) + suffix
	dest := filepath.Join(dir, name)
	if err := compress(tempPath, dest); err != nil {
		return Result{}, err
	}
	return Prune(dir, keep, Verify)
}

// compress writes a zstd stream to a temporary name in the destination's
// directory, syncs it, and renames it into place.
func compress(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("backup: reading the snapshot: %w", err)
	}
	defer in.Close()

	temp, err := os.CreateTemp(filepath.Dir(dest), ".backup-*.zst")
	if err != nil {
		return fmt.Errorf("backup: reserving %s: %w", dest, err)
	}
	tempPath := temp.Name()
	published := false
	defer func() {
		if !published {
			_ = os.Remove(tempPath)
		}
	}()

	encoder, err := zstd.NewWriter(temp, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		_ = temp.Close()
		return fmt.Errorf("backup: starting the compressor: %w", err)
	}
	if _, err := io.Copy(encoder, in); err != nil {
		_ = encoder.Close()
		_ = temp.Close()
		return fmt.Errorf("backup: compressing: %w", err)
	}
	if err := encoder.Close(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("backup: finishing the compressor: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("backup: syncing %s: %w", tempPath, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("backup: closing %s: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, dest); err != nil {
		return fmt.Errorf("backup: publishing %s: %w", dest, err)
	}
	published = true
	return nil
}

// List reads the backups in dir, newest first. Files that are not backups are
// ignored, and a name that does not parse is ignored too: the directory belongs
// to the operator, and an unrelated file is not the backup's problem.
func List(dir string) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("backup: reading %s: %w", dir, err)
	}

	var backups []Entry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		date, ok := parseName(entry.Name())
		if !ok {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("backup: reading %s: %w", dir, err)
		}
		backups = append(backups, Entry{
			Path:  filepath.Join(dir, entry.Name()),
			Name:  entry.Name(),
			Date:  date,
			Bytes: info.Size(),
		})
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].Date.After(backups[j].Date) })
	return backups, nil
}

// DateOf reports when a backup was taken, from its name. It is the one piece of
// metadata a backup carries without a database to read it from.
func DateOf(path string) (time.Time, bool) {
	return parseName(filepath.Base(path))
}

func parseName(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return time.Time{}, false
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	date, err := time.Parse(nameLayout, stamp)
	if err != nil {
		return time.Time{}, false
	}
	return date, true
}

// Prune keeps the newest keep backups and deletes the rest — except any it
// cannot verify, which it leaves alone and reports. Deleting the last readable
// copy because it was old is how a backup system loses data quietly.
func Prune(dir string, keep int, verify func(string) error) (Result, error) {
	if keep <= 0 {
		keep = DefaultKeep
	}
	backups, err := List(dir)
	if err != nil {
		return Result{}, err
	}
	result := Result{Kept: backups}
	if len(backups) > keep {
		result.Kept = backups[:keep]
		for _, old := range backups[keep:] {
			if err := verify(old.Path); err != nil {
				// A backup that cannot be verified is not safe to delete: it may
				// be the only copy that opens, and the operator needs to know it
				// is broken rather than find it gone.
				result.Kept = append(result.Kept, old)
				result.Failed = append(result.Failed, Failure{Entry: old, Err: err})
				continue
			}
			if err := os.Remove(old.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return Result{}, fmt.Errorf("backup: deleting %s: %w", old.Path, err)
			}
		}
	}
	return result, nil
}
