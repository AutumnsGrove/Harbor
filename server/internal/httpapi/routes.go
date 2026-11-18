package httpapi

import (
	"net/http"

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
// interrupt?". Phase 0 has nothing to interrupt; the queue and importer fill
// this in.
type busyResponse struct {
	Busy bool `json:"busy"`
}

func (s *Server) busy(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, busyResponse{Busy: false})
}

// statusResponse is the header-less exemption: counts only, never content. It
// stays a stub until the store lands, which is why it reads no database today —
// the contract is that it answers when the database is broken.
type statusResponse struct {
	Status        string  `json:"status"`
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptimeSeconds"`
	Items         int     `json:"items"`
	Pending       int     `json:"pending"`
	Failed        int     `json:"failed"`
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{
		Status:        "ok",
		Version:       build.Version(),
		UptimeSeconds: s.now().Sub(s.started).Seconds(),
	})
}
