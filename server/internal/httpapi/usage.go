package httpapi

import (
	"net/http"
	"strconv"

	"github.com/AutumnsGrove/Harbor/server/internal/store"
)

// /api/usage answers the Settings → AI usage panel in one round trip: what the
// models cost this month and in total, the split by surface and model, a day
// series for the sparkline, what a save costs on average, and what is still
// queued so the panel can say "1,476 left ≈ $2.20" before the money is spent
// (issue #52).

const (
	defaultUsageDays = 30
	maxUsageDays     = 365
)

type usageResponse struct {
	store.UsageReport

	// BacklogItems is the enrichment queue: what "remaining" means for the
	// estimate. BudgetUSD is the denominator for the month bar, so the panel
	// never hardcodes it in Svelte.
	BacklogItems int     `json:"backlogItems"`
	BudgetUSD    float64 `json:"budgetUsd"`
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	if !s.requireLibrary(w) {
		return
	}

	days := defaultUsageDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxUsageDays {
			writeError(w, http.StatusBadRequest, "days must be a whole number between 1 and 365")
			return
		}
		days = parsed
	}

	report, err := s.library.UsageReport(r.Context(), s.now(), days)
	if err != nil {
		s.log.Error("reading usage", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read usage")
		return
	}

	// The backlog is the same count /api/status reports, and a failure here must
	// not cost the panel its numbers.
	var backlog int
	// The window is only there for saved_this_week, which this handler does not
	// read; Pending is the backlog and does not depend on it.
	if counts, err := s.library.Counts(r.Context(), s.now().AddDate(0, 0, -7)); err != nil {
		s.log.Warn("usage: backlog unavailable", "error", err)
	} else {
		backlog = counts.Pending
	}

	writeJSON(w, http.StatusOK, usageResponse{
		UsageReport:  report,
		BacklogItems: backlog,
		BudgetUSD:    s.cfg.AIBudgetUSD,
	})
}
