package httpapi

import (
	"net/http"
	"path/filepath"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

// healthResponse is deliberately two fields. /healthz is the Docker
// HEALTHCHECK, the Meridian vital and the update watcher's only signal, so it
// must be answerable by a process with no database, no queue and no network
// (design.md §3).
type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// healthz touches nothing: no store, no filesystem, no lock. Every later phase
// must keep it that way; the update path depends on this answer being cheap and
// honest.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: build.Version()})
}

// versionResponse is what Settings shows in the footer and what the update
// check compares against.
type versionResponse struct {
	Version    string `json:"version"`
	Deployment string `json:"deployment"`
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, versionResponse{
		Version:    build.Version(),
		Deployment: string(s.cfg.Deployment),
	})
}

// busyResponse answers "is the app in the middle of something a restart would
// interrupt?". The queue has not landed yet; an import has, and an update that
// restarts mid-file would stop it — resumably, but there is no hurry.
type busyResponse struct {
	Busy bool `json:"busy"`
}

func (s *Server) busy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, busyResponse{Busy: s.imports.running()})
}

// statusResponse is the header-less exemption: counts only, never content. It
// answers when the database is broken, so every number here is optional in
// practice — a count that cannot be read is a log line and a zero, not a
// failed request (design.md §3).
//
// The key names are the contract in docs/meridian.md ("What /api/status must
// expose"): snake_case, because that is what the tile config and the deploy-day
// checklist pin. QueueDepth is the enrichment queue: queued plus enriching.
// MirrorPending and LastLinkCheck are phase 6 and phase 11; they are present and
// zero/null so the tile never has to guess whether a field is missing or the
// feature is.
type statusResponse struct {
	Status        string     `json:"status"`
	Version       string     `json:"version"`
	UptimeSeconds float64    `json:"uptimeSeconds"`
	ItemsTotal    int        `json:"items_total"`
	QueueDepth    int        `json:"queue_depth"`
	Failed        int        `json:"failed"`
	SavedThisWeek int        `json:"saved_this_week"`
	MirrorPending int        `json:"mirror_pending"`
	DiskUsedPct   *float64   `json:"disk_used_pct,omitempty"`
	LastBackup    *time.Time `json:"last_backup"`
	LastLinkCheck *time.Time `json:"last_link_check"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	response := statusResponse{
		Status:        "ok",
		Version:       build.Version(),
		UptimeSeconds: s.now().Sub(s.started).Seconds(),
	}
	if s.library != nil {
		counts, err := s.library.Counts(r.Context(), s.now().AddDate(0, 0, -7))
		if err != nil {
			// The endpoint still answers: it is the one a broken database must
			// not be able to take down alongside the app.
			s.log.Warn("status counts unavailable", "error", err)
		} else {
			response.ItemsTotal = counts.Items
			response.QueueDepth = counts.Pending
			response.Failed = counts.Failed
			response.SavedThisWeek = counts.SavedThisWeek
		}
	}
	// The filesystem numbers are the box's, not the database's, so they are read
	// even when the library is unusable — that is the state a full disk creates.
	// A stat that fails omits the field rather than claiming 0% full.
	response.DiskUsedPct = diskUsedPct(s.cfg.DataDir)
	response.LastBackup = lastBackupAt(filepath.Join(s.cfg.DataDir, "backups"))

	writeJSON(w, http.StatusOK, response)
}
