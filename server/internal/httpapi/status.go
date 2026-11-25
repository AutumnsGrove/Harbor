package httpapi

import (
	"syscall"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/backup"
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

// lastBackupAt is the date of the newest snapshot, read through backup.List.
// That package owns the rule for what a backup is — `harbor-<date>.db.zst` — and
// reads the date from the name, so the compression's own temp files
// (`.backup-*.zst` in the same directory) are not mistaken for a finished
// snapshot, and an unrelated file cannot become one.
//
// nil means there has never been a backup, which is the fact the tile should
// show rather than a confident zero.
func lastBackupAt(dir string) *time.Time {
	backups, err := backup.List(dir)
	if err != nil || len(backups) == 0 {
		// A missing directory means no backup has ever been taken, which is the
		// same answer as a directory with nothing in it. A read error is rare
		// enough that a null field is better than a failed status page.
		return nil
	}
	// List is newest first and each Date comes from the name.
	newest := backups[0].Date.UTC()
	return &newest
}
