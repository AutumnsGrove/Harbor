package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

func testConfig(t *testing.T) build.Config {
	t.Helper()
	return build.Config{
		Deployment:   build.DeploymentDev,
		Port:         8080,
		DataDir:      t.TempDir(),
		UpdateDir:    t.TempDir(),
		AllowedLogin: "autumn@grove.place",
		AllowedHosts: []string{"localhost", "127.0.0.1", "[::1]"},
	}
}

// handlerFor wraps the full chain, which is what every test should exercise:
// middleware is where the security rules live, so bypassing it would test a
// program that never runs.
func handlerFor(t *testing.T, cfg build.Config) http.Handler {
	t.Helper()
	return New(cfg, slog.New(slog.DiscardHandler), testAssets()).Handler()
}

func request(h http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "localhost"
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func authed(h http.Handler, method, target string) *httptest.ResponseRecorder {
	return request(h, method, target, map[string]string{identityHeader: "autumn@grove.place"})
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body %q is not JSON: %v", rec.Body.String(), err)
	}
	return body
}

func TestHealthz(t *testing.T) {
	h := handlerFor(t, testConfig(t))

	start := time.Now()
	rec := request(h, http.MethodGet, "/healthz", nil)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200", rec.Code)
	}
	if got, want := rec.Header().Get("Content-Type"), "application/json; charset=utf-8"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}

	// design.md §3: exactly two fields, so the Docker HEALTHCHECK, the Meridian
	// vital and the watcher can all read it with no schema.
	body := decode(t, rec)
	if len(body) != 2 {
		t.Errorf("GET /healthz body = %v, want exactly two fields", body)
	}
	if body["status"] != "ok" {
		t.Errorf(`status = %v, want "ok"`, body["status"])
	}
	if body["version"] != build.Version() {
		t.Errorf("version = %v, want %q", body["version"], build.Version())
	}

	// The handler does no I/O, so this is a generous bound: it exists to catch
	// a future phase adding a database call to the liveness probe.
	if elapsed > 10*time.Millisecond {
		t.Errorf("GET /healthz took %s, want under 10ms", elapsed)
	}
}

func TestHealthzNeedsNoFrontendAndNoStore(t *testing.T) {
	// The server is built with an empty asset filesystem and no database at
	// all, which is what a broken restore looks like.
	s := New(testConfig(t), nil, emptyAssets())
	rec := request(s.Handler(), http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz without a frontend = %d, want 200", rec.Code)
	}
}

func TestIdentityLock(t *testing.T) {
	h := handlerFor(t, testConfig(t))

	tests := []struct {
		name   string
		method string
		path   string
		login  string
		want   int
	}{
		{name: "API without a header", method: http.MethodGet, path: "/api/version", want: http.StatusForbidden},
		{name: "API with a wrong login", method: http.MethodGet, path: "/api/version", login: "someone@else.example", want: http.StatusForbidden},
		{name: "API with a prefix of the real login", method: http.MethodGet, path: "/api/version", login: "autumn@grove", want: http.StatusForbidden},
		{name: "API with the real login", method: http.MethodGet, path: "/api/version", login: "autumn@grove.place", want: http.StatusOK},
		{name: "healthz without a header", method: http.MethodGet, path: "/healthz", want: http.StatusOK},
		{name: "healthz with a wrong login is refused", method: http.MethodGet, path: "/healthz", login: "someone@else.example", want: http.StatusForbidden},
		{name: "status without a header", method: http.MethodGet, path: "/api/status", want: http.StatusOK},
		{name: "status with a wrong login is refused", method: http.MethodGet, path: "/api/status", login: "someone@else.example", want: http.StatusForbidden},
		{name: "an unwritten route is still locked", method: http.MethodGet, path: "/api/items", want: http.StatusForbidden},
		{name: "a write route without a header", method: http.MethodPost, path: "/api/update/check", want: http.StatusForbidden},
		{name: "the shell is locked too", method: http.MethodGet, path: "/", want: http.StatusForbidden},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			headers := map[string]string{}
			if test.login != "" {
				headers[identityHeader] = test.login
			}
			rec := request(h, test.method, test.path, headers)
			if rec.Code != test.want {
				t.Fatalf("%s %s with login %q = %d, want %d (body %s)",
					test.method, test.path, test.login, rec.Code, test.want, rec.Body.String())
			}
		})
	}
}

func TestIdentityLockIgnoresThePeerAddress(t *testing.T) {
	// Inside Docker every caller is the bridge gateway, so 127.0.0.1 means
	// nothing (spike S10). This asserts the lock does not fall back to it.
	h := handlerFor(t, testConfig(t))
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	req.Host = "localhost"
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("loopback request with no identity = %d, want 403", rec.Code)
	}
}

func TestDevNoAuthBypass(t *testing.T) {
	cfg := testConfig(t)
	cfg.DevNoAuth = true
	h := handlerFor(t, cfg)

	if rec := request(h, http.MethodGet, "/api/version", nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/version with DevNoAuth = %d, want 200", rec.Code)
	}
}

func TestHostCheck(t *testing.T) {
	h := handlerFor(t, testConfig(t))

	tests := []struct {
		host string
		want int
	}{
		{host: "localhost", want: http.StatusOK},
		{host: "localhost:8897", want: http.StatusOK},
		{host: "LocalHost", want: http.StatusOK},
		{host: "127.0.0.1:8080", want: http.StatusOK},
		{host: "[::1]:8080", want: http.StatusOK},
		{host: "harbor.tailnet.ts.net", want: http.StatusMisdirectedRequest},
		{host: "evil.example", want: http.StatusMisdirectedRequest},
		{host: "localhost.evil.example", want: http.StatusMisdirectedRequest},
	}

	for _, test := range tests {
		t.Run(test.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Host = test.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("Host %q = %d, want %d", test.host, rec.Code, test.want)
			}
		})
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := handlerFor(t, testConfig(t))

	tests := []struct {
		name   string
		method string
		path   string
		allow  string
	}{
		{name: "POST to a read route", method: http.MethodPost, path: "/api/version", allow: "GET"},
		{name: "GET to a write route", method: http.MethodGet, path: "/api/update/start", allow: "POST"},
		{name: "POST to healthz", method: http.MethodPost, path: "/healthz", allow: "GET"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rec := authed(h, test.method, test.path)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 405", test.method, test.path, rec.Code)
			}
			if got := rec.Header().Get("Allow"); got != test.allow {
				t.Errorf("Allow = %q, want %q", got, test.allow)
			}
			if body := decode(t, rec); body["error"] == nil {
				t.Errorf("405 body = %v, want a JSON error", body)
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := handlerFor(t, testConfig(t))
	rec := request(h, http.MethodGet, "/healthz", nil)

	headers := rec.Header()
	csp := headers.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "object-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP = %q, want it to contain %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Errorf("CSP = %q, must not allow inline script or style", csp)
	}
	if got := headers.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want none: there is no cross-origin caller", got)
	}
	if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := headers.Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache on the API", got)
	}
}

func TestRequestID(t *testing.T) {
	h := handlerFor(t, testConfig(t))

	generated := request(h, http.MethodGet, "/healthz", nil).Header().Get(requestIDHeader)
	if len(generated) != 16 || strings.Trim(generated, "0123456789abcdef") != "" {
		t.Errorf("generated request id = %q, want 16 hex characters", generated)
	}

	if got := request(h, http.MethodGet, "/healthz", map[string]string{requestIDHeader: "abc-123_ok"}).Header().Get(requestIDHeader); got != "abc-123_ok" {
		t.Errorf("inbound request id = %q, want abc-123_ok", got)
	}

	junk := request(h, http.MethodGet, "/healthz", map[string]string{requestIDHeader: "bad id!"}).Header().Get(requestIDHeader)
	if junk == "bad id!" || len(junk) != 16 {
		t.Errorf("junk inbound request id produced %q, want a fresh id", junk)
	}
}

func TestRecover(t *testing.T) {
	s := New(testConfig(t), nil, testAssets())
	h := s.recover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := request(h, http.MethodGet, "/", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panicking handler = %d, want 500", rec.Code)
	}
	if body := decode(t, rec); body["error"] != "internal error" {
		t.Errorf("body = %v, want an internal error", body)
	}
}

func TestLogging(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h := New(testConfig(t), logger, testAssets()).Handler()

	authed(h, http.MethodGet, "/api/version")

	var line map[string]any
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(logs.String())))
	if err := decoder.Decode(&line); err != nil {
		t.Fatalf("log line is not JSON: %v (%q)", err, logs.String())
	}
	if line["msg"] != "http request" {
		t.Errorf("msg = %v, want %q", line["msg"], "http request")
	}
	if line["path"] != "/api/version" || line["status"] != float64(http.StatusOK) {
		t.Errorf("log line = %v, want path and status", line)
	}
	if id, ok := line["request_id"].(string); !ok || len(id) != 16 {
		t.Errorf("request_id = %v, want a generated id", line["request_id"])
	}
}

func TestStatic(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantBody   string
		wantCache  string
		wantJSON   bool
	}{
		{name: "the shell", method: http.MethodGet, path: "/", wantStatus: 200, wantBody: "Harbor shell", wantCache: "no-cache"},
		{name: "a head request", method: http.MethodHead, path: "/", wantStatus: 200, wantCache: "no-cache"},
		{name: "a SPA route", method: http.MethodGet, path: "/item/42", wantStatus: 200, wantBody: "Harbor shell", wantCache: "no-cache"},
		{name: "a nested SPA route", method: http.MethodGet, path: "/spaces/reading", wantStatus: 200, wantBody: "Harbor shell"},
		{name: "a hashed asset", method: http.MethodGet, path: "/_app/immutable/app.js", wantStatus: 200, wantBody: "console.log", wantCache: "public, max-age=31536000, immutable"},
		{name: "a missing asset is not the shell", method: http.MethodGet, path: "/_app/immutable/gone.js", wantStatus: 404, wantJSON: true},
		{name: "a missing file is not the shell", method: http.MethodGet, path: "/favicon.png", wantStatus: 404, wantJSON: true},
		{name: "an unknown API route is JSON", method: http.MethodGet, path: "/api/nope", wantStatus: 404, wantJSON: true},
		{name: "the API root is JSON", method: http.MethodGet, path: "/api", wantStatus: 404, wantJSON: true},
		{name: "a write to the shell is refused", method: http.MethodPost, path: "/", wantStatus: 405, wantJSON: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := handlerFor(t, testConfig(t))
			rec := authed(h, test.method, test.path)

			if rec.Code != test.wantStatus {
				t.Fatalf("%s %s = %d, want %d", test.method, test.path, rec.Code, test.wantStatus)
			}
			if test.wantBody != "" && !strings.Contains(rec.Body.String(), test.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), test.wantBody)
			}
			if test.wantCache != "" {
				if got := rec.Header().Get("Cache-Control"); got != test.wantCache {
					t.Errorf("Cache-Control = %q, want %q", got, test.wantCache)
				}
			}
			if test.wantJSON {
				if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
					t.Errorf("Content-Type = %q, want JSON", got)
				}
			}
			if test.method == http.MethodHead && rec.Body.Len() != 0 {
				t.Errorf("HEAD body = %q, want empty", rec.Body.String())
			}
		})
	}
}

func TestStaticWithoutAFrontendBuild(t *testing.T) {
	s := New(testConfig(t), nil, emptyAssets())
	rec := authed(s.Handler(), http.MethodGet, "/")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET / with no frontend = %d, want 503", rec.Code)
	}
	if body := decode(t, rec); body["error"] == nil {
		t.Errorf("body = %v, want a JSON error", body)
	}
}

func TestStatusIsCountsOnly(t *testing.T) {
	h := handlerFor(t, testConfig(t))
	rec := request(h, http.MethodGet, "/api/status", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/status = %d, want 200", rec.Code)
	}
	body := decode(t, rec)
	// The exemption is counts and the box's own vitals — nothing a library row
	// could reach: no title, no URL, no tag, no path beyond the data directory's
	// fill level and the newest snapshot's timestamp (issue #60).
	for key := range body {
		switch key {
		case "status", "version", "uptimeSeconds",
			"items", "queueDepth", "failed", "savedThisWeek",
			"mirrorPending", "diskUsedPct", "lastBackup", "lastLinkCheck":
		default:
			t.Errorf("GET /api/status exposes %q; the header-less exemption is counts only", key)
		}
	}
}
