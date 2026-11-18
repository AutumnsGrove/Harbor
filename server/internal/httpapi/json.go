package httpapi

import (
	"encoding/json"
	"net/http"
)

// writeJSON is the only way the handlers emit a body: every response, including
// every error, is the same shape of JSON, so the frontend has one decoding path
// (design.md §9).
func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		// A handler that cannot encode its own payload is a programming error,
		// not a client error; say so without echoing the value.
		writeRaw(w, http.StatusInternalServerError, []byte(`{"error":"response could not be encoded"}`))
		return
	}
	writeRaw(w, status, body)
}

// writeRaw emits bytes that are already valid JSON, used to pass the watcher's
// available.json/status.json through without reparsing and reserialising it.
func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeError is the error half of the contract: {"error": "..."} with the status
// doing the classification.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
