package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"regexp"
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

// securityHeaders is a strict, no-CORS policy. The frontend is same-origin by
// construction (it is served by this binary), so there is no reason to open
// anything up. Do not add 'unsafe-inline' here to make a build problem go away:
// fix the build (design.md §3).
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := w.Header()
		headers.Set("Content-Security-Policy", strings.Join([]string{
			"default-src 'self'",
			"script-src 'self'",
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
