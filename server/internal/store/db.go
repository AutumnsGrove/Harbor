package store

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// maxReportedProblems bounds the log line: a badly damaged file can produce
// thousands of integrity_check rows, and the first few say what is wrong.
const maxReportedProblems = 10

// integrityBudget is generous on purpose: the potato's disk is slow and the
// library only grows. A check that times out is logged, never fatal.
const integrityBudget = 5 * time.Minute

// integrityCheck runs PRAGMA integrity_check and returns the problems it
// reported, none when the database is sound. An error means the check itself
// could not run to the end, which on a damaged file is also a finding.
func integrityCheck(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return problems, err
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	return problems, rows.Err()
}

// checkOnBoot runs the integrity check and logs the outcome. It never fails
// the boot: a library that is damaged but readable is more useful to its owner
// than one that refuses to start, and the snapshot taken before each update is
// what recovery uses (plans/02-core-library.md, tasks; design.md §3).
func checkOnBoot(db *sql.DB, log *slog.Logger) {
	// Its own budget: integrity_check reads the whole file, so it must not
	// share the short deadline Open uses for ping and pragmas.
	ctx, cancel := context.WithTimeout(context.Background(), integrityBudget)
	defer cancel()
	start := time.Now()
	problems, err := integrityCheck(ctx, db)
	switch {
	case err != nil:
		log.Error("integrity check could not complete", "err", err, "reported", problems)
	case len(problems) > 0:
		if len(problems) > maxReportedProblems {
			problems = problems[:maxReportedProblems]
		}
		log.Error("integrity check found problems", "problems", problems)
	default:
		log.Info("integrity check ok", "took", time.Since(start).Round(time.Millisecond))
	}
}
