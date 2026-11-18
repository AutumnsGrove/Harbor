// Package httpapi is Harbor's whole HTTP surface: the JSON API the SvelteKit
// frontend calls, the liveness endpoint Docker and Meridian probe, the update
// signal endpoints the settings page writes, and the embedded frontend itself.
//
// Two rules shape everything here (design.md §3):
//
//   - The update endpoints write signal files with constant names. The app never
//     names an image and never lets a request choose a path.
//   - Identity is the Tailscale-User-Login header, not the peer address. Inside
//     Docker the peer is always the bridge gateway, so "is it loopback" cannot
//     tell the tailnet from the host (spike S10).
package httpapi

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

const (
	// identityHeader is set and overwritten by `tailscale serve`. A tailnet
	// client cannot forge it (spike S10).
	identityHeader = "Tailscale-User-Login"

	// requestIDHeader is echoed for log correlation. Inbound values are only
	// trusted if they look like ours.
	requestIDHeader = "X-Request-ID"

	// Signal file names in HARBOR_UPDATE_DIR. They are constants, joined to the
	// directory and nothing else: no request value reaches a path.
	updateCheckFile     = "check"
	updateRequestFile   = "requested"
	updateAvailableFile = "available.json"
	updateStatusFile    = "status.json"

	// latestImage is the literal the watcher expects in `requested`. The app
	// must never write an image name or a digest: the watcher owns the
	// repository (design.md §3).
	latestImage = "latest"
)

// Server holds everything a handler needs. It deliberately holds no database:
// phase 0's endpoints must answer with the store absent, closed or corrupt,
// which is the same condition a broken restore creates.
type Server struct {
	cfg    build.Config
	log    *slog.Logger
	assets fs.FS
	files  http.Handler

	// scriptHashes are the CSP hashes of the frontend's inline bootstrap
	// scripts, read from the build's csp.json. Empty in tests and in a binary
	// built without a frontend.
	scriptHashes []string

	started time.Time
	now     func() time.Time
}

// New builds a Server around an embedded frontend. assets is web.Assets() in the
// binary and an in-memory fs in tests.
func New(cfg build.Config, log *slog.Logger, assets fs.FS) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{
		cfg:          cfg,
		log:          log,
		assets:       assets,
		files:        http.FileServerFS(assets),
		scriptHashes: loadScriptHashes(assets),
		started:      time.Now(),
		now:          time.Now,
	}
}

// Handler returns the whole middleware chain. Order matters and mirrors
// design.md §3: recover → request id → structured log → security headers →
// Host check → identity lock → handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.only(http.MethodGet, s.healthz))
	mux.HandleFunc("/api/version", s.only(http.MethodGet, s.version))
	mux.HandleFunc("/api/busy", s.only(http.MethodGet, s.busy))
	mux.HandleFunc("/api/status", s.only(http.MethodGet, s.status))
	mux.HandleFunc("/api/update/check", s.only(http.MethodPost, s.updateCheck))
	mux.HandleFunc("/api/update/available", s.only(http.MethodGet, s.updateAvailable))
	mux.HandleFunc("/api/update/start", s.only(http.MethodPost, s.updateStart))
	mux.HandleFunc("/api/update/status", s.only(http.MethodGet, s.updateStatus))
	mux.HandleFunc("/", s.static)

	return s.recover(s.requestID(s.logging(s.securityHeaders(s.hostCheck(s.identityLock(mux))))))
}

// only enforces the method itself instead of letting ServeMux answer 405 in
// plain text, so a client never has to parse two error formats.
func (s *Server) only(method string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			w.Header().Set("Allow", method)
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handler(w, r)
	}
}
