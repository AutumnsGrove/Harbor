package httpapi

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// requestIDPattern is the only inbound request id worth echoing: short, opaque
// and safe to put in a log line.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// recover turns a panic into a logged 500 instead of a dropped connection. Every
// later phase adds code that parses hostile input; this is the floor, not the
// plan for correctness.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.Error("handler panicked",
					"request_id", requestIDFrom(r),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", recovered,
				)
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestID gives every request an id: the caller's when it is well formed, ours
// otherwise. It is returned in the response so a browser console entry can be
// matched to a server log line.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !requestIDPattern.MatchString(id) {
			id = newRequestID()
		}
		r.Header.Set(requestIDHeader, id)
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// rand.Read never fails in practice; a timestamp keeps the field
		// populated rather than empty if it ever does.
		return "t" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(buf[:])
}

func requestIDFrom(r *http.Request) string {
	return r.Header.Get(requestIDHeader)
}

// logging emits one structured line per request to stdout. Health probes are
// logged at debug: Docker asks every 30 s and nobody reads that at info.
func (s *Server) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		recorder := &statusRecorder{ResponseWriter: w}

		next.ServeHTTP(recorder, r)

		level := slog.LevelInfo
		if r.URL.Path == "/healthz" {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "http request",
			"request_id", requestIDFrom(r),
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.statusCode(),
			"bytes", recorder.bytes,
			"duration_ms", float64(s.now().Sub(start).Microseconds())/1000,
			// Diagnostic only. Inside Docker this is the bridge gateway for
			// every caller, so it is never used as identity (spike S10).
			"remote", r.RemoteAddr,
		)
	})
}

// statusRecorder remembers what the handler did without changing it. Unwrap
// keeps http.ResponseController (and FileServer's sendfile path) working.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	written, err := r.ResponseWriter.Write(body)
	r.bytes += written
	return written, err
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// ── compress ────────────────────────────────────────────────────────────────

// gzipMinBody is where compression starts paying for itself. The gzip header
// and trailer are about 20 bytes and a small JSON answer gains nothing but a
// broken Content-Length.
const gzipMinBody = 1024

// compress gzips text responses on the wire. It sits inside logging, so the
// `bytes` in the request log is what actually crossed the link, and inside
// securityHeaders, so the CSP and Cache-Control set there survive untouched.
//
// Everything a browser needs to paint the first screen is text (~169 KB of
// JS/CSS/HTML in the phase-2 build gzips to ~64 KB), and the potato is behind a
// relayed tailnet link where the bytes are the latency. Already-compressed
// formats, small bodies, HEAD, Range and bodiless statuses pass through
// untouched: compressing a partial body would corrupt it.
func (s *Server) compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}

		cw := &compressWriter{ResponseWriter: w, request: r}
		// finish runs even if the handler panics, so a partly written gzip stream
		// still gets its trailer instead of leaving a truncated body behind.
		defer func() {
			if err := cw.finish(); err != nil {
				// A client that hung up mid-response is normal; the request log
				// already recorded the bytes that made it. Not worth a warning.
				s.log.Debug("compress: write failed",
					"request_id", requestIDFrom(r),
					"path", r.URL.Path,
					"error", err,
				)
			}
		}()
		next.ServeHTTP(cw, r)
	})
}

// compressWriter holds the status and body until it knows whether the response
// is worth compressing: the Content-Type arrives with the headers and the size
// only after the first writes. Until then it buffers, which is why the threshold
// is one small body and not the whole response.
type compressWriter struct {
	http.ResponseWriter
	request *http.Request

	status  int
	decided bool
	pending bytes.Buffer

	// writer is non-nil exactly when the body is being gzipped. err is sticky:
	// once the stream is broken, every later write reports the same failure.
	writer *gzip.Writer
	err    error
}

func (w *compressWriter) WriteHeader(status int) {
	// The real header is not sent yet; decide does that, once it knows whether
	// Content-Length has to be dropped and Vary has to be added.
	if w.status == 0 {
		w.status = status
	}
}

func (w *compressWriter) Write(body []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if !w.decided {
		w.pending.Write(body)
		if w.pending.Len() < gzipMinBody {
			// Too early to tell. The handler still gets the count it asked
			// for; nothing has been written anywhere yet.
			return len(body), nil
		}
		w.decide()
		return len(body), w.err
	}
	return w.writeThrough(body)
}

func (w *compressWriter) writeThrough(body []byte) (int, error) {
	if w.writer != nil {
		n, err := w.writer.Write(body)
		if err != nil {
			w.err = err
		}
		return n, err
	}
	n, err := w.ResponseWriter.Write(body)
	if err != nil {
		w.err = err
	}
	return n, err
}

// decide commits the status and whatever is buffered, choosing between a
// gzipped body and the handler's own bytes. It runs once.
func (w *compressWriter) decide() {
	if w.decided {
		return
	}
	w.decided = true

	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	headers := w.ResponseWriter.Header()

	contentType := headers.Get("Content-Type")
	if contentType == "" {
		// Same rule net/http applies when a handler sets nothing.
		contentType = http.DetectContentType(w.pending.Bytes())
		headers.Set("Content-Type", contentType)
	}

	compressible := bodyAllowed(status) && compressibleType(contentType)
	if compressible {
		// Anything caching in front must keep the two encodings apart even
		// when this client did not ask for gzip.
		headers.Add("Vary", "Accept-Encoding")
	}

	if compressible && w.pending.Len() >= gzipMinBody && acceptsGzip(w.request) {
		headers.Set("Content-Encoding", "gzip")
		// The handler's length describes the body it wrote, not this one.
		headers.Del("Content-Length")
		w.ResponseWriter.WriteHeader(status)
		w.writer = gzip.NewWriter(w.ResponseWriter)
		if _, err := w.writer.Write(w.pending.Bytes()); err != nil {
			w.err = err
		}
		w.pending.Reset()
		return
	}

	w.ResponseWriter.WriteHeader(status)
	if w.pending.Len() > 0 {
		if _, err := w.ResponseWriter.Write(w.pending.Bytes()); err != nil {
			w.err = err
		}
	}
	w.pending.Reset()
}

// finish flushes a body that never reached the threshold and closes the gzip
// stream so its trailer is written.
func (w *compressWriter) finish() error {
	w.decide()
	if w.writer == nil {
		return w.err
	}
	if err := w.writer.Close(); err != nil && w.err == nil {
		w.err = err
	}
	return w.err
}

// Flush keeps http.ResponseController working for a handler that streams. The
// buffered body is committed first, so a flush cannot arrive before its headers.
func (w *compressWriter) Flush() {
	w.decide()
	if w.writer != nil {
		_ = w.writer.Flush()
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the real writer, the same way
// statusRecorder does.
func (w *compressWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// bodyAllowed reports whether a status can carry a body at all: 1xx, 204 and
// 304 must not be wrapped.
func bodyAllowed(status int) bool {
	if status < 200 {
		return false
	}
	return status != http.StatusNoContent && status != http.StatusNotModified
}

// compressibleType refuses the formats where gzip can only make the response
// bigger, and event streams, which must reach the client line by line.
func compressibleType(contentType string) bool {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))

	switch {
	case strings.HasPrefix(contentType, "image/"),
		strings.HasPrefix(contentType, "video/"),
		strings.HasPrefix(contentType, "audio/"):
		return false
	}

	switch contentType {
	case "font/woff2", "text/event-stream",
		"application/zip", "application/zstd", "application/x-zstd",
		"application/gzip", "application/x-gzip":
		return false
	}
	return true
}

// acceptsGzip reads the header, including q=0, which means "not this one".
func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(name, "gzip") && !strings.EqualFold(name, "*") {
			continue
		}
		if q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q="); ok {
			if weight, err := strconv.ParseFloat(q, 64); err == nil && weight <= 0 {
				continue
			}
		}
		return true
	}
	return false
}

// securityHeaders is a strict, no-CORS policy. The frontend is same-origin by
// construction (it is served by this binary), so there is no reason to open
// anything up. Do not add 'unsafe-inline' here to make a build problem go away:
// fix the build (design.md §3).
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := w.Header()
		headers.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"script-src " + s.scriptSources(),
			"style-src 'self'",
			"img-src 'self' data:",
			"font-src 'self'",
			"connect-src 'self'",
			"media-src 'self'",
			"form-action 'self'",
			"frame-ancestors 'none'",
			"base-uri 'none'",
			"object-src 'none'",
		}, "; "))
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("Cross-Origin-Opener-Policy", "same-origin")
		headers.Set("Cross-Origin-Resource-Policy", "same-origin")
		headers.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		// Static assets are content-addressed by the SvelteKit build, so they
		// can be cached forever; everything else (the shell, the API) must not
		// be. The frontend stage owns the hashes (design.md §3).
		if strings.HasPrefix(r.URL.Path, "/_app/immutable/") {
			headers.Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			headers.Set("Cache-Control", "no-cache")
		}

		next.ServeHTTP(w, r)
	})
}

// hostCheck refuses a Host that is not in HARBOR_ALLOWED_HOSTS. The app is
// reachable only through `tailscale serve` and the loopback publish, so any
// other name is either a misconfiguration or a DNS-rebinding attempt.
func (s *Server) hostCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hostAllowed(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, "host not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hostAllowed(hostport string) bool {
	host := hostOnly(hostport)
	for _, allowed := range s.cfg.AllowedHosts {
		if host == allowed {
			return true
		}
	}
	return false
}

// hostOnly drops the port so the loopback publish (127.0.0.1:8897) and
// `tailscale serve` (harbor.tailnet.ts.net) can share one allowlist. IPv6
// literals arrive bare from SplitHostPort and bracketed in a Host header, so
// they are normalised to the bracketed form the allowlist uses.
func hostOnly(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return host
}

// identityLock is the whole access control model in one function: the request
// must carry the owner's Tailscale login, with exactly two header-less
// exemptions that expose counts, never content (design.md §3, spike S10).
func (s *Server) identityLock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.DevNoAuth {
			next.ServeHTTP(w, r)
			return
		}

		login := r.Header.Get(identityHeader)
		if headerlessAllowed(r) && login == "" {
			next.ServeHTTP(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(login), []byte(s.cfg.AllowedLogin)) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		if login == "" {
			writeError(w, http.StatusForbidden, "identity required")
			return
		}
		writeError(w, http.StatusForbidden, "identity not recognised")
	})
}

// headerlessAllowed lists the only requests that may run without an identity
// header: the liveness probe and the status counts. A *wrong* login is still
// refused on both.
func headerlessAllowed(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	switch r.URL.Path {
	case "/healthz", "/api/status":
		return true
	default:
		return false
	}
}

// scriptSources is 'self' plus the exact hashes of the frontend's inline
// bootstrap scripts. It is a method because the list comes from the build, not
// from a constant: the policy and the artifact are generated together.
func (s *Server) scriptSources() string {
	sources := "'self'"
	for _, hash := range s.scriptHashes {
		sources += " '" + hash + "'"
	}
	return sources
}
