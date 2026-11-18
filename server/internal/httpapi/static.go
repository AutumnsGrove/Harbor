package httpapi

import (
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// static serves the embedded SvelteKit build and the SPA fallback. The frontend
// is not a separate origin: it is these bytes, behind the same identity lock and
// Host check as the API (design.md §3).
//
// A missing index.html means the binary was built without a frontend (a bare
// `go build` in development). That is a 503 with a readable reason, not a blank
// page.
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	// /api/* that reached the catch-all does not exist. It must never fall
	// through to index.html: a client asking for JSON should get JSON.
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	cleaned := path.Clean("/" + r.URL.Path)
	name := strings.TrimPrefix(cleaned, "/")
	if name != "" {
		if file, err := s.assets.Open(name); err == nil {
			_ = file.Close()
			s.files.ServeHTTP(w, r)
			return
		}
		// A missing asset is a 404, not the shell. Only extensionless paths are
		// routes the SPA can own.
		if strings.HasPrefix(cleaned, "/_app/") || path.Ext(name) != "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
	}

	s.index(w, r)
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	body, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "frontend not built into this binary")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(body)
}
