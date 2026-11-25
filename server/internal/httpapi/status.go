package httpapi

import (
	"os"
	"strings"
	"syscall"
	"time"
)

// The two numbers /api/status reads from the box rather than the database
// (issue #60). Both are best-effort: a status page that cannot answer because a
// stat failed is worse than one that omits the field.

// diskUsedPct reports how full the data filesystem is, rounded to one decimal.
// nil means the filesystem could not be read, and is omitted from the response
// rather than reported as 0% full.
func diskUsedPct(dir string) *float64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return nil
	}
	total := float64(stat.Blocks) * float64(stat.Bsize)
	if total <= 0 {
		return nil
	}
	free := float64(stat.Bavail) * float64(stat.Bsize)
	used := (1 - free/total) * 100
	// One decimal is plenty for a bar, and keeps float noise out of the JSON.
	used = float64(int(used*10+0.5)) / 10
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	return &used
}

// lastBackupAt is the mtime of the newest snapshot in the backup directory.
// nil means there has never been one — which is true of the potato today, and
// is the fact the tile should show rather than a confident zero.
func lastBackupAt(dir string) *time.Time {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var newest time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".zst") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return nil
	}
	utc := newest.UTC()
	return &utc
}
