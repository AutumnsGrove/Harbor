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
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
	"github.com/AutumnsGrove/Harbor/server/internal/cover"
	"github.com/AutumnsGrove/Harbor/server/internal/store"
	"github.com/AutumnsGrove/Harbor/server/internal/workerclient"
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

	// library is nil in phase-0 tests and in a binary built without a database.
	// /api/status answers either way; a count it cannot read is a log line, not
	// a failed request (design.md §3).
	library *store.Store

	// pending is nil when no Worker is configured. /api/pending then answers an
	// empty list, which is true: there is no queue to show.
	pending PendingSource

	// imports owns the one import the process is running (Settings → Import).
	imports *importManager

	// covers serves the stored page image. Nil answers 404 for every cover,
	// which is the same thing a failed fetch does (issue #49).
	covers *cover.Store
}

// PendingSource is the Worker's read-only pending list (design.md §5). It is an
// interface so the handler cannot claim, read a job's content or reach R2.
type PendingSource interface {
	Pending(ctx context.Context) ([]workerclient.Job, error)
}

// Option adds a dependency to a Server. It is a functional option so the
// phase-0 tests and the update endpoints keep their three-argument New.
type Option func(*Server)

// WithStore lets /api/status report the library's counts. Passing nil is the
// same as not calling it.
func WithStore(library *store.Store) Option {
	return func(s *Server) { s.library = library }
}

// WithCovers lets /api/items/{id}/cover serve the page image. Without it every
// card keeps its placeholder.
func WithCovers(covers *cover.Store) Option {
	return func(s *Server) { s.covers = covers }
}

// WithPending lets /api/pending show the saves that are still at the Worker.
func WithPending(source PendingSource) Option {
	return func(s *Server) { s.pending = source }
}

// New builds a Server around an embedded frontend. assets is web.Assets() in the
// binary and an in-memory fs in tests.
func New(cfg build.Config, log *slog.Logger, assets fs.FS, opts ...Option) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{
		cfg:          cfg,
		log:          log,
		assets:       assets,
		files:        http.FileServerFS(assets),
		scriptHashes: loadScriptHashes(assets),
		started:      time.Now(),
		now:          time.Now,
	}
	s.imports = newImportManager(s.log)
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Handler returns the whole middleware chain. Order matters and mirrors
// design.md §3: recover → request id → structured log → security headers →
// compress → Host check → identity lock → handler. Compression sits inside the
// log so its byte count is what crossed the wire, and inside the headers so the
// CSP and Cache-Control are already set when the body is wrapped.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.only(http.MethodGet, s.healthz))
	mux.HandleFunc("/api/version", s.only(http.MethodGet, s.version))
	mux.HandleFunc("/api/busy", s.only(http.MethodGet, s.busy))
	mux.HandleFunc("/api/status", s.only(http.MethodGet, s.status))
	mux.HandleFunc("/api/usage", s.only(http.MethodGet, s.usage))
	mux.HandleFunc("/api/items", s.items)
	mux.HandleFunc("/api/items/lookup", s.only(http.MethodGet, s.lookupItem))
	mux.HandleFunc("/api/items/", s.itemByPath)
	mux.HandleFunc("/api/pending", s.only(http.MethodGet, s.pendingJobs))
	mux.HandleFunc("/api/attention", s.only(http.MethodGet, s.attention))
	mux.HandleFunc("/api/import", s.only(http.MethodPost, s.importStart))
	mux.HandleFunc("/api/import/status", s.only(http.MethodGet, s.importStatus))
	mux.HandleFunc("/api/import/cancel", s.only(http.MethodPost, s.importCancel))
	mux.HandleFunc("/api/update/check", s.only(http.MethodPost, s.updateCheck))
	mux.HandleFunc("/api/update/available", s.only(http.MethodGet, s.updateAvailable))
	mux.HandleFunc("/api/update/start", s.only(http.MethodPost, s.updateStart))
	mux.HandleFunc("/api/update/status", s.only(http.MethodGet, s.updateStatus))
	mux.HandleFunc("/", s.static)

	return s.chain(mux)
}

// chain is the middleware order itself, in one place. Handler hands it the mux;
// tests hand it a handler they need to drive through the real stack (a panic,
// say) without rebuilding the order and letting the copy drift.
func (s *Server) chain(next http.Handler) http.Handler {
	return s.recover(s.requestID(s.logging(s.securityHeaders(s.compress(s.hostCheck(s.identityLock(next)))))))
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
