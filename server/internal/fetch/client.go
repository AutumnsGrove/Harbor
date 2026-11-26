// Package fetch retrieves a page for enrichment. It is a security boundary:
// Harbor runs inside the tailnet and fetches URLs the owner saved from the
// public internet, so every address is checked at dial time, not only when the
// URL is parsed (design.md §3, plans/03 test 1).
//
// The body goes to a temp file rather than a []byte. Enrichment is one at a
// time on a small box, and a 4 MB HTML page parsed into a DOM is the only real
// memory spike in the system (design.md §6). Streaming to disk keeps the bytes
// out of the heap until the parser needs them.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Defaults are design.md §6's numbers: a 5 MB cap and a 20 s timeout.
const (
	DefaultMaxBytes     int64 = 5 << 20
	DefaultTimeout            = 20 * time.Second
	DefaultMaxRedirects       = 5
	dialTimeout               = 10 * time.Second
)

var (
	// ErrBlocked is returned for an address the guard refuses. Callers report
	// it; they never fall back to a raw dial.
	ErrBlocked = errors.New("fetch: address is not allowed")
	// ErrTooLarge is returned before or during the read when the body exceeds
	// the cap.
	ErrTooLarge = errors.New("fetch: response is larger than the cap")
	// ErrTooManyRedirects bounds a redirect loop.
	ErrTooManyRedirects = errors.New("fetch: too many redirects")
	// ErrUnsupportedURL is a scheme or shape Harbor will not fetch.
	ErrUnsupportedURL = errors.New("fetch: unsupported URL")
	// ErrDeadLink is a name that does not resolve: the host is gone, which is a
	// dead link rather than a bad save (issue #76).
	ErrDeadLink = errors.New("fetch: the host does not resolve")
)

// BrowserUserAgent is what Harbor presents. It is a browser's, deliberately:
// a shop that hangs any non-browser client (uniqlo) answers this one in under a
// second, and most of the 403s were punishing `Harbor/0`. The reversal and its
// accepted cost are in design §6 and open-questions P1. One constant, so the
// next reversal is one line.
const BrowserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// HTTPError is a non-2xx answer. Permanent tells the retry policy whether
// another attempt could help: a 404 will not start working, a 503 might.
type HTTPError struct {
	URL    string
	Status int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("fetch: %s answered %d", e.URL, e.Status)
}

// Permanent reports whether the status is one that retrying cannot fix.
func (e *HTTPError) Permanent() bool {
	switch e.Status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusGone,
		http.StatusUnavailableForLegalReasons:
		return true
	}
	return false
}

// Walled reports a site refusing this client rather than the page being broken:
// a paywall, a bot wall, a sign-in. The save is still hers, so the pipeline
// files it instead of parking it (design §6).
func (e *HTTPError) Walled() bool {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return true
	}
	return false
}

// Dead reports the page being gone: it was here, it is not. `link_status`'s
// vocabulary, reached by enrichment as well as by phase 11's checker.
func (e *HTTPError) Dead() bool {
	return e.Status == http.StatusNotFound || e.Status == http.StatusGone
}

// RateLimited reports a site that asked us to come back later. Unlike the two
// above it is retryable, on its own longer budget (design §6).
func (e *HTTPError) RateLimited() bool { return e.Status == http.StatusTooManyRequests }

// Page is a fetched response on disk. Close removes the temp file; a caller
// that forgets leaks one file, never a descriptor.
type Page struct {
	// URL is the final address after redirects. Relative links resolve
	// against it.
	URL *url.URL
	// ContentType is the header as sent, parameters included.
	ContentType string
	// Size is the number of bytes written.
	Size int64
	// Body is the temp file, positioned at the start.
	Body io.ReadSeeker

	path string
}

// Close closes and removes the temp file.
func (p *Page) Close() error {
	err := p.Body.(*os.File).Close()
	if removeErr := os.Remove(p.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	return err
}

// Client is safe for concurrent use.
type Client struct {
	http         *http.Client
	maxBytes     int64
	maxRedirects int
	userAgent    string

	// allowUnsafe skips both the URL check and the dial-time IP check. It is
	// only ever set by tests, which serve pages from 127.0.0.1; production has
	// no option for it. TestDefaultClientBlockLocalhost proves the default.
	allowUnsafe bool
}

// Option changes a Client before its transport is built.
type Option func(*Client)

// WithMaxBytes sets the body cap.
func WithMaxBytes(n int64) Option {
	return func(c *Client) {
		if n > 0 {
			c.maxBytes = n
		}
	}
}

// WithTimeout sets the whole-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if d > 0 {
			c.http.Timeout = d
		}
	}
}

// WithMaxRedirects sets how many redirects one fetch may follow.
func WithMaxRedirects(n int) Option {
	return func(c *Client) {
		if n >= 0 {
			c.maxRedirects = n
		}
	}
}

// WithUserAgent sets the User-Agent. The default is BrowserUserAgent: presenting
// a browser is a deliberate decision with a cost (design §6, open-questions P1),
// and a page that then serves no readable text is filed as a wall rather than
// called a broken save.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if strings.TrimSpace(ua) != "" {
			c.userAgent = ua
		}
	}
}

// New builds a client with design.md §6's caps. The dialer's Control hook sees
// the address actually being connected to, which is what makes a DNS answer
// that changes between lookups irrelevant: a private address is refused
// whether it came from the URL or from the resolver.
func New(opts ...Option) *Client {
	c := &Client{
		maxBytes:     DefaultMaxBytes,
		maxRedirects: DefaultMaxRedirects,
		userAgent:    BrowserUserAgent,
	}
	c.http = &http.Client{Timeout: DefaultTimeout}

	for _, opt := range opts {
		opt(c)
	}

	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
		Control:   c.dialControl,
	}
	c.http.Transport = &http.Transport{
		// No proxy: a proxy resolves the destination itself, so the Control
		// hook would see the proxy's address and the guard would be bypassed.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: DefaultTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	c.http.CheckRedirect = c.checkRedirect
	return c
}

// Get fetches rawURL into a temp file. The caller owns the Page and must Close
// it. No cookies, no credentials and no proxy are involved.
func (c *Client) Get(ctx context.Context, rawURL string) (*Page, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedURL, err)
	}
	if err := c.validateURL(u); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnsupportedURL, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.8,*/*;q=0.1")
	req.Header.Set("Accept-Language", "en;q=0.9,*;q=0.5")

	resp, err := c.http.Do(req)
	if err != nil {
		// A name that does not resolve is the host being gone, not a fault in
		// Harbor; it files as a dead link. The guard's own refusal (a sinkholed
		// 0.0.0.0 answer, a private address) is a different error on purpose.
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, fmt.Errorf("%w: %s", ErrDeadLink, dnsErr.Name)
		}
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &HTTPError{URL: resp.Request.URL.String(), Status: resp.StatusCode}
	}
	// A declared length over the cap is refused without reading a byte.
	if resp.ContentLength > c.maxBytes {
		return nil, fmt.Errorf("%w: %d bytes declared, cap %d", ErrTooLarge, resp.ContentLength, c.maxBytes)
	}

	tmp, err := os.CreateTemp("", "harbor-fetch-*")
	if err != nil {
		return nil, fmt.Errorf("fetch: temp file: %w", err)
	}
	// One byte past the cap distinguishes "at the cap" from "over it".
	written, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, c.maxBytes+1))
	if copyErr == nil && written > c.maxBytes {
		copyErr = fmt.Errorf("%w: more than %d bytes", ErrTooLarge, c.maxBytes)
	}
	if copyErr == nil {
		_, copyErr = tmp.Seek(0, io.SeekStart)
	}
	if copyErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("fetch: %w", copyErr)
	}

	return &Page{
		URL:         resp.Request.URL,
		ContentType: resp.Header.Get("Content-Type"),
		Size:        written,
		Body:        tmp,
		path:        tmp.Name(),
	}, nil
}

// checkRedirect validates every hop with the same URL rules and bounds the
// chain. The dial guard runs again for each hop's address; this is the cheaper
// half that catches a bad port or hostname before a connection is attempted.
func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > c.maxRedirects {
		return ErrTooManyRedirects
	}
	return c.validateURL(req.URL)
}
