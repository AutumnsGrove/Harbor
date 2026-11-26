package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// plans/03 tests 1–4. The guard is tested both as a policy (table) and through
// a real request; the permissive flag is what lets httptest stand in for the
// public internet, and TestDefaultClientRefusesLoopback proves it is off by
// default.

func newTestClient(t *testing.T, opts ...Option) *Client {
	t.Helper()
	c := New(opts...)
	c.allowUnsafe = true
	t.Cleanup(c.http.CloseIdleConnections)
	return c
}

func parseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}

func TestGuardRefusesPrivateAddresses(t *testing.T) {
	c := New()
	refused := []string{
		"http://localhost/x",
		"http://localhost./x",
		"http://api.localhost/x",
		"http://127.0.0.1/",
		"http://127.0.0.53/",
		"http://[::1]/",
		"http://0.0.0.0/",
		"http://10.1.2.3/",
		"http://172.16.0.1/",
		"http://172.31.255.255/",
		"http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data",
		"http://100.100.100.100/",
		"http://100.64.0.1/",
		"http://[fd7a:115c:a1e0::1]/",
		"http://[fd00::1]/",
		"http://[::ffff:127.0.0.1]/",
		"http://[::ffff:192.168.0.1]/",
		"http://printer.local/",
		"http://potato.ts.net/",
		"http://example.com:8080/",
		"http://example.com:22/",
		"file:///etc/passwd",
		"ftp://example.com/file",
		"http://user:pass@example.com/",
		"http:///nohost",
	}
	for _, raw := range refused {
		t.Run(raw, func(t *testing.T) {
			if err := c.validateURL(parseURL(t, raw)); err == nil {
				t.Errorf("validateURL(%q) = nil, want a refusal", raw)
			}
		})
	}

	allowed := []string{
		"https://example.com/",
		"https://example.com:443/",
		"http://example.com:80/",
		"http://93.184.216.34/",
		"https://[2606:2800:220:1:248:1893:25c8:1946]/",
	}
	for _, raw := range allowed {
		t.Run("allows "+raw, func(t *testing.T) {
			if err := c.validateURL(parseURL(t, raw)); err != nil {
				t.Errorf("validateURL(%q) = %v, want nil", raw, err)
			}
		})
	}
}

// The dial hook is the half that sees the resolved address; a hostname that
// resolved to a private IP never gets a connection.
func TestDialControlChecksTheResolvedAddress(t *testing.T) {
	c := New()
	for _, address := range []string{
		"10.0.0.1:443",
		"127.0.0.1:80",
		"169.254.169.254:80",
		"100.100.100.100:443",
		"[fd7a:115c:a1e0::1]:443",
		"[::ffff:192.168.1.1]:443",
	} {
		if err := c.dialControl("tcp", address, nil); !errors.Is(err, ErrBlocked) {
			t.Errorf("dialControl(%q) = %v, want ErrBlocked", address, err)
		}
	}
	for _, address := range []string{"93.184.216.34:443", "[2606:2800:220:1:248:1893:25c8:1946]:443"} {
		if err := c.dialControl("tcp", address, nil); err != nil {
			t.Errorf("dialControl(%q) = %v, want nil", address, err)
		}
	}
	if err := c.dialControl("tcp", "not-an-address", nil); !errors.Is(err, ErrBlocked) {
		t.Errorf("dialControl(garbage) = %v, want ErrBlocked", err)
	}
}

func TestDefaultClientRefusesLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "this must never be read")
	}))
	defer server.Close()

	_, err := New().Get(context.Background(), server.URL)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("Get(%s) = %v, want ErrBlocked", server.URL, err)
	}
}

func TestGetWritesTheBodyToADiskFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body><p>hello</p></body></html>")
	}))
	defer server.Close()

	page, err := newTestClient(t).Get(context.Background(), server.URL+"/x?q=1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, err := io.ReadAll(page.Body)
	if err != nil {
		t.Fatalf("reading the page: %v", err)
	}
	if string(body) != "<html><body><p>hello</p></body></html>" {
		t.Errorf("body = %q", body)
	}
	if page.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", page.Size, len(body))
	}
	if !strings.HasPrefix(page.ContentType, "text/html") {
		t.Errorf("ContentType = %q", page.ContentType)
	}
	if page.URL.Query().Get("q") != "1" {
		t.Errorf("URL = %s, want the requested URL", page.URL)
	}

	path := page.path
	if err := page.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the temp file %s still exists after Close", path)
	}
}

func TestDeclaredContentLengthOverTheCapIsRefused(t *testing.T) {
	big := strings.Repeat("x", 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, big) // net/http sets Content-Length for a small body
	}))
	defer server.Close()

	_, err := newTestClient(t, WithMaxBytes(64)).Get(context.Background(), server.URL)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Get = %v, want ErrTooLarge", err)
	}
}

func TestChunkedBodyIsCutAtTheCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, _ := w.(http.Flusher)
		for range 4 {
			fmt.Fprint(w, strings.Repeat("y", 64))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	page, err := newTestClient(t, WithMaxBytes(100)).Get(context.Background(), server.URL)
	if !errors.Is(err, ErrTooLarge) {
		if page != nil && page.path != "" {
			_ = page.Close()
		}
		t.Fatalf("Get = %v, want ErrTooLarge", err)
	}
}

func TestTimeoutStopsASlowServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	start := time.Now()
	_, err := newTestClient(t, WithTimeout(80*time.Millisecond)).Get(context.Background(), server.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the timeout took %v", elapsed)
	}
}

func TestRedirectsAreBounded(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer server.Close()

	_, err := newTestClient(t, WithMaxRedirects(3)).Get(context.Background(), server.URL)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("Get = %v, want ErrTooManyRedirects", err)
	}
	if got := hits.Load(); got != 4 {
		t.Errorf("server answered %d requests, want 4 (the first plus 3 redirects)", got)
	}
}

// The redirect check re-runs the URL policy for every hop; this calls it the
// way net/http would, with a private target.
func TestTheRedirectCheckRefusesAPrivateTarget(t *testing.T) {
	c := New()
	req := &http.Request{URL: parseURL(t, "http://169.254.169.254/latest/meta-data")}
	if err := c.checkRedirect(req, nil); !errors.Is(err, ErrBlocked) {
		t.Errorf("checkRedirect to link-local = %v, want ErrBlocked", err)
	}
}

func TestHTTPErrorsCarryPermanence(t *testing.T) {
	tests := []struct {
		status    int
		permanent bool
	}{
		{http.StatusNotFound, true},
		{http.StatusGone, true},
		{http.StatusForbidden, true},
		{http.StatusTooManyRequests, false},
		{http.StatusServiceUnavailable, false},
		{http.StatusBadGateway, false},
	}
	for _, test := range tests {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(test.status)
		}))
		_, err := newTestClient(t).Get(context.Background(), server.URL)
		server.Close()

		var httpErr *HTTPError
		if !errors.As(err, &httpErr) {
			t.Errorf("status %d: error = %v, want *HTTPError", test.status, err)
			continue
		}
		if httpErr.Permanent() != test.permanent {
			t.Errorf("status %d: Permanent() = %v, want %v", test.status, httpErr.Permanent(), test.permanent)
		}
	}
}

func TestTheUserAgentNamesTheBuild(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
	}))
	defer server.Close()

	if _, err := newTestClient(t, WithUserAgent("Harbor/r88.abc123 (+https://example.com)")).Get(context.Background(), server.URL); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.HasPrefix(seen, "Harbor/r88.abc123") {
		t.Errorf("User-Agent = %q", seen)
	}
}

// issue #76, design §6: a page that will not serve this client and a page that
// is gone are different answers, and neither of them is "broken".
func TestAnHTTPErrorSaysWhichKindOfNoItWas(t *testing.T) {
	cases := []struct {
		status int
		walled bool
		dead   bool
		rate   bool
	}{
		{http.StatusUnauthorized, true, false, false},
		{http.StatusForbidden, true, false, false},
		{http.StatusUnavailableForLegalReasons, true, false, false},
		{http.StatusNotFound, false, true, false},
		{http.StatusGone, false, true, false},
		{http.StatusTooManyRequests, false, false, true},
		{http.StatusInternalServerError, false, false, false},
		{http.StatusOK, false, false, false},
	}
	for _, c := range cases {
		err := &HTTPError{URL: "https://example.com/", Status: c.status}
		if got := err.Walled(); got != c.walled {
			t.Errorf("%d: Walled() = %v, want %v", c.status, got, c.walled)
		}
		if got := err.Dead(); got != c.dead {
			t.Errorf("%d: Dead() = %v, want %v", c.status, got, c.dead)
		}
		if got := err.RateLimited(); got != c.rate {
			t.Errorf("%d: RateLimited() = %v, want %v", c.status, got, c.rate)
		}
	}
}

// A host that does not resolve at all is a dead link, not a bad URL: the save
// was fine, the site is not there any more. The SSRF guard's own refusal stays
// a separate error, because a sinkholed answer (0.0.0.0) arrives as an address
// and must not read as "the site is gone" (issues #76, #77).
func TestAHostThatDoesNotResolveIsADeadLink(t *testing.T) {
	c := newTestClient(t)
	_, err := c.Get(context.Background(), "https://there-is-no-such-host.invalid/page")
	if !errors.Is(err, ErrDeadLink) {
		t.Fatalf("Get() error = %v, want ErrDeadLink", err)
	}
	if errors.Is(err, ErrBlocked) {
		t.Error("a name that does not resolve was reported as a blocked address")
	}
}

// The fetcher presents a browser's User-Agent (open-questions P1): it is what
// makes a shop that hangs `curl/8` answer at all. One constant, so the next
// reversal is one line.
func TestTheDefaultUserAgentIsABrowser(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		fmt.Fprint(w, "<html><body>hello</body></html>")
	}))
	defer server.Close()

	c := newTestClient(t)
	page, err := c.Get(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer page.Close()

	if seen != BrowserUserAgent {
		t.Fatalf("User-Agent = %q, want the browser constant", seen)
	}
	if strings.Contains(strings.ToLower(seen), "harbor") || strings.Contains(strings.ToLower(seen), "curl") {
		t.Errorf("User-Agent = %q; a page that blocks unknown clients is being told exactly what we are", seen)
	}
	if !strings.HasPrefix(seen, "Mozilla/5.0") {
		t.Errorf("User-Agent = %q, want a Mozilla/5.0 shape", seen)
	}
}
