package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Snapshot writes a consistent copy of the live database to dest.
//
// The sequence is the one the updater depends on (design.md §3):
//
//	VACUUM INTO <temp>  →  PRAGMA integrity_check  →  fsync  →  rename  →  fsync(dir)
//
// dest never exists in a partial state: either it is the previous good snapshot
// or the new complete one. If any step fails, the old dest is untouched and the
// temp file is removed, so a failed update cannot leave a rollback target that
// looks valid and is not.
func (s *Store) Snapshot(ctx context.Context, dest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create snapshot dir %s: %w", dir, err)
	}

	// VACUUM INTO refuses to overwrite, so reserve a unique name and remove it
	// again before SQLite creates the file.
	reserved, err := os.CreateTemp(dir, ".snapshot-*.db")
	if err != nil {
		return fmt.Errorf("reserve a snapshot name in %s: %w", dir, err)
	}
	temp := reserved.Name()
	_ = reserved.Close()
	if err := os.Remove(temp); err != nil {
		return fmt.Errorf("reserve a snapshot name in %s: %w", dir, err)
	}
	// Any failure below removes the temp file; success renames it away.
	defer func() { _ = os.Remove(temp) }()

	// VACUUM INTO takes a read lock for its duration and writes a fully
	// consistent copy, including a WAL that has not been checkpointed yet. It
	// works while another process holds a write connection.
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", temp); err != nil {
		return fmt.Errorf("snapshot %s: %w", s.path, err)
	}

	if err := Verify(temp); err != nil {
		return fmt.Errorf("snapshot %s: %w", s.path, err)
	}
	if err := syncFile(temp); err != nil {
		return fmt.Errorf("sync snapshot %s: %w", temp, err)
	}
	if err := os.Rename(temp, dest); err != nil {
		return fmt.Errorf("publish snapshot %s: %w", dest, err)
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync snapshot dir %s: %w", dir, err)
	}
	return nil
}

// Verify opens a database file read-only and runs PRAGMA integrity_check. The
// updater calls it through Snapshot, and phase 12's restore drill reuses it.
func Verify(path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("open %s for verification: %w", path, err)
	}
	defer db.Close()

	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity_check on %s: %w", path, err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity_check on %s: %s", path, result)
	}
	return nil
}

func syncFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// syncDir makes the rename durable. Some filesystems refuse fsync on a
// directory; that is not a snapshot failure, because the rename itself already
// happened.
func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	if err := handle.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
