package httpapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
)

// Issue #50: the potato's first load is text over a relayed tailnet link, so the
// bytes are the latency. These tests are the acceptance list from the issue:
// compress what is worth compressing, leave everything else byte-identical, and
// never let Content-Length disagree with the body.

// gzipText is larger than gzipMinBody and repetitive, so gzip wins clearly.
var gzipText = []byte(strings.Repeat("harbor gzip acceptance ", 200))

func compressServer(t *testing.T) *Server {
	t.Helper()
	return New(testConfig(t), slog.New(slog.DiscardHandler), testAssets())
}

// bodyHandler answers with a fixed type and body, and sets Content-Length the
// way the static handlers do.
func bodyHandler(contentType string, body []byte, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(status)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	})
}

// serveCompressed runs one request through the middleware alone, so a test can
// see the handler's bytes and not the whole chain.
func serveCompressed(t *testing.T, h http.Handler, method string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/thing.json", nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	compressServer(t).compress(h).ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, body []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = reader.Close() }()
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzipped body: %v", err)
	}
	return plain
}

func TestCompressGzipsACompressibleBody(t *testing.T) {
	rec := serveCompressed(t, bodyHandler("application/json; charset=utf-8", gzipText, http.StatusOK), http.MethodGet, map[string]string{
		"Accept-Encoding": "gzip, deflate, br",
	})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want it removed: it describes the uncompressed body", got)
	}
	if got := gunzip(t, rec.Body.Bytes()); !bytes.Equal(got, gzipText) {
		t.Errorf("decompressed body does not match what the handler wrote")
	}
	if rec.Body.Len() >= len(gzipText) {
		t.Errorf("compressed body is %d bytes, not smaller than %d", rec.Body.Len(), len(gzipText))
	}
}

func TestCompressLeavesAClientWithoutGzipAlone(t *testing.T) {
	rec := serveCompressed(t, bodyHandler("application/json", gzipText, http.StatusOK), http.MethodGet, nil)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
	// It is still a response this middleware would compress, so a cache must
	// keep the two variants apart.
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), gzipText) {
		t.Errorf("body changed for a client that did not accept gzip")
	}
}

func TestCompressTreatsQZeroAsNoGzip(t *testing.T) {
	rec := serveCompressed(t, bodyHandler("text/css", gzipText, http.StatusOK), http.MethodGet, map[string]string{
		"Accept-Encoding": "gzip;q=0, identity",
	})

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none for q=0", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), gzipText) {
		t.Errorf("body changed for q=0")
	}
}

func TestCompressSkipsAlreadyCompressedFormats(t *testing.T) {
	cases := []string{
		"font/woff2",
		"image/png",
		"image/svg+xml",
		"video/mp4",
		"audio/mpeg",
		"application/zstd",
		"application/zip",
	}
	for _, contentType := range cases {
		t.Run(contentType, func(t *testing.T) {
			rec := serveCompressed(t, bodyHandler(contentType, gzipText, http.StatusOK), http.MethodGet, map[string]string{
				"Accept-Encoding": "gzip",
			})

			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Errorf("Content-Encoding = %q, want none", got)
			}
			if got := rec.Header().Get("Vary"); got != "" {
				t.Errorf("Vary = %q, want none: this is never compressed", got)
			}
			if !bytes.Equal(rec.Body.Bytes(), gzipText) {
				t.Errorf("body changed for %s", contentType)
			}
			if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(gzipText)) {
				t.Errorf("Content-Length = %q, want the handler's own", got)
			}
		})
	}
}

func TestCompressSkipsSmallBodies(t *testing.T) {
	small := []byte(`{"status":"ok","version":"r1.abc"}`)
	if len(small) >= gzipMinBody {
		t.Fatalf("the fixture is %d bytes; it must be under the threshold", len(small))
	}

	rec := serveCompressed(t, bodyHandler("application/json", small, http.StatusOK), http.MethodGet, map[string]string{
		"Accept-Encoding": "gzip",
	})

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none under %d bytes", got, gzipMinBody)
	}
	if !bytes.Equal(rec.Body.Bytes(), small) {
		t.Errorf("small body changed")
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(small)) {
		t.Errorf("Content-Length = %q, want %d", got, len(small))
	}
}

func TestCompressSkipsBodilessStatuses(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusNotModified} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			rec := serveCompressed(t, bodyHandler("application/json", nil, status), http.MethodGet, map[string]string{
				"Accept-Encoding": "gzip",
			})
			if rec.Code != status {
				t.Errorf("status = %d, want %d", rec.Code, status)
			}
			if got := rec.Header().Get("Content-Encoding"); got != "" {
				t.Errorf("Content-Encoding = %q, want none", got)
			}
		})
	}
}

func TestCompressSkipsHEAD(t *testing.T) {
	rec := serveCompressed(t, bodyHandler("application/json", gzipText, http.StatusOK), http.MethodHead, map[string]string{
		"Accept-Encoding": "gzip",
	})

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none for HEAD", got)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body = %d bytes, want 0", rec.Body.Len())
	}
}

func TestCompressSkipsRangeRequests(t *testing.T) {
	rec := serveCompressed(t, bodyHandler("application/json", gzipText, http.StatusPartialContent), http.MethodGet, map[string]string{
		"Accept-Encoding": "gzip",
		"Range":           "bytes=0-99",
	})

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none: a compressed partial body is corrupt", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), gzipText) {
		t.Errorf("Range body changed")
	}
}

// The request log has to count compressed bytes, which is only true if compress
// is inside logging. The full chain is the proof.
func TestHandlerLogsTheCompressedSize(t *testing.T) {
	const payload = "console.log('harbor gzip acceptance payload');\n"
	assets := fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><html><body>Harbor</body></html>")},
		"_app/immutable/app.js": {Data: []byte(strings.Repeat(payload, 200))},
	}

	var logged bytes.Buffer
	h := New(testConfig(t), slog.New(slog.NewJSONHandler(&logged, nil)), assets).Handler()

	rec := request(h, http.MethodGet, "/_app/immutable/app.js", map[string]string{
		identityHeader:    "autumn@grove.place",
		"Accept-Encoding": "gzip",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET the asset = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}

	type logLine struct {
		Msg   string `json:"msg"`
		Path  string `json:"path"`
		Bytes int    `json:"bytes"`
	}
	var found *logLine
	decoder := json.NewDecoder(&logged)
	for {
		var line logLine
		err := decoder.Decode(&line)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode log line: %v", err)
		}
		if line.Msg == "http request" {
			found = &line
		}
	}

	if found == nil || found.Path != "/_app/immutable/app.js" {
		t.Fatalf("no request log line for the asset: %+v", found)
	}
	if found.Bytes != rec.Body.Len() {
		t.Errorf("logged bytes = %d, response body = %d; compress is outside logging", found.Bytes, rec.Body.Len())
	}
}

// index.html sets Content-Length explicitly (static.go). Compressing it must
// drop that header rather than leave a length for a different body.
func TestHandlerDropsContentLengthOnTheShell(t *testing.T) {
	assets := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><html><body>" + strings.Repeat("harbor ", 500) + "</body></html>")},
	}
	h := New(testConfig(t), slog.New(slog.DiscardHandler), assets).Handler()

	rec := request(h, http.MethodGet, "/", map[string]string{
		identityHeader:    "autumn@grove.place",
		"Accept-Encoding": "gzip",
	})

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Errorf("Content-Length = %q, want none", got)
	}
	if !bytes.Contains(gunzip(t, rec.Body.Bytes()), []byte("harbor")) {
		t.Errorf("decompressed shell lost its body")
	}
}

func TestCompressUnwrapKeepsResponseControllerWorking(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The first write is what lets compress choose its encoding; the flush
		// after it proves the controller can still reach the real writer.
		if _, err := w.Write(gzipText); err != nil {
			t.Errorf("write: %v", err)
		}
		// If Unwrap were missing this would fail with "feature not supported".
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("ResponseController.Flush through compress: %v", err)
		}
	})

	rec := serveCompressed(t, handler, http.MethodGet, map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Contains(gunzip(t, rec.Body.Bytes()), []byte("harbor")) {
		t.Errorf("flushed body did not reach the client")
	}
}

// pi-review on #56: a panic before the handler's first write must still reach
// recover as a 500. finish used to commit 200 while the panic unwound, which
// made the response an unchangeable 200 with the error body inside it.
func TestPanicBeforeFirstWriteStaysA500(t *testing.T) {
	s := compressServer(t)
	h := s.chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("nil map write")
	}))

	rec := request(h, http.MethodGet, "/thing.json", map[string]string{
		identityHeader: "autumn@grove.place",
	})

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: compress committed the response before recover ran", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal error") {
		t.Errorf("body = %q, want recover's JSON error", rec.Body.String())
	}
}

// A handler that answers nothing at all still gets net/http's implicit 200, not
// a status the middleware invented while deciding whether to compress.
func TestCompressLeavesAnEmptyResponseAlone(t *testing.T) {
	rec := serveCompressed(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), http.MethodGet, map[string]string{
		"Accept-Encoding": "gzip",
	})

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want empty", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q, want none", got)
	}
}

// pi-review on #56: an explicit gzip entry wins over the wildcard, even when the
// wildcard comes first. RFC 9110 §12.5.3.
func TestAcceptsGzipFollowsRFC9110Precedence(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"identity", false},
		{"gzip", true},
		{"deflate, gzip", true},
		{"*", true},
		{"gzip;q=0.5, *", true},
		{"gzip;q=0, *", false},
		{"*, gzip;q=0", false},
		{"*;q=0, gzip", true},
		{"*;q=0, gzip;q=0", false},
		{"deflate, *;q=0, gzip;q=0.1", true},
		{"gzip;q=bogus", false},
	}
	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.header != "" {
				r.Header.Set("Accept-Encoding", tc.header)
			}
			if got := acceptsGzip(r); got != tc.want {
				t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}
