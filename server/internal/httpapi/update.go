package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

// The update path is four small endpoints and a directory of signal files
// (design.md §3):
//
//	POST /api/update/check      → update-signal/check        (path unit: resolve :latest)
//	GET  /api/update/available  ← update-signal/available.json
//	POST /api/update/start      → update-signal/requested    (contents: "latest")
//	GET  /api/update/status     ← update-signal/status.json
//
// The names below are constants. Nothing from the request reaches the path, so
// traversal is not "prevented" here — it is unrepresentable.

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.writeSignal(updateCheckFile, ""); err != nil {
		s.signalFailed(w, r, "check", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "checking"})
}

func (s *Server) updateStart(w http.ResponseWriter, r *http.Request) {
	// The literal, never an image name or digest. The watcher resolves and pins
	// the artifact; if the app could name one, a compromised app could name its
	// own (design.md §3).
	if err := s.writeSignal(updateRequestFile, latestImage+"\n"); err != nil {
		s.signalFailed(w, r, "start", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}

func (s *Server) updateAvailable(w http.ResponseWriter, r *http.Request) {
	s.passthroughSignal(w, r, updateAvailableFile, `{"available":false}`)
}

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	s.passthroughSignal(w, r, updateStatusFile, `{"status":"unknown"}`)
}

// writeSignal creates the update directory if the volume is missing (a fresh
// host, before install.sh has run) and writes one constant file.
func (s *Server) writeSignal(name, content string) error {
	if err := os.MkdirAll(s.cfg.UpdateDir, 0o755); err != nil {
		return fmt.Errorf("create update dir: %w", err)
	}
	path := filepath.Join(s.cfg.UpdateDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// readSignal returns (nil, nil) when the watcher has not written the file yet.
// "Not yet" is a normal state, not an error: the settings page must render
// before the first check.
func (s *Server) readSignal(name string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(s.cfg.UpdateDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// passthroughSignal forwards the watcher's JSON untouched. It is validated, not
// remodelled: the watcher owns that shape, and the app must not quietly drop a
// field it does not know about yet.
func (s *Server) passthroughSignal(w http.ResponseWriter, r *http.Request, name, absent string) {
	data, err := s.readSignal(name)
	if err != nil {
		s.log.Error("could not read update signal", "file", name, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read "+name)
		return
	}
	if len(data) == 0 {
		writeRaw(w, http.StatusOK, []byte(absent))
		return
	}
	if !json.Valid(data) {
		s.log.Error("update signal is not valid JSON", "file", name)
		writeError(w, http.StatusInternalServerError, name+" is not valid JSON")
		return
	}
	writeRaw(w, http.StatusOK, data)
}

func (s *Server) signalFailed(w http.ResponseWriter, r *http.Request, action string, err error) {
	s.log.Error("could not write update signal", "action", action, "dir", s.cfg.UpdateDir, "error", err)
	writeError(w, http.StatusInternalServerError, "could not write the update signal")
}
