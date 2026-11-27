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

// Defaults are design.md §6's numbers: a 5 MB cap and a 20 s timeout for a
// page, and a 40 MB one for a document, because a 5 MB ceiling is right for a
// page and wrong for a paper — arxiv alone will never fit under it, and the ten
// rows on the box that failed on the cap declared up to 38.7 MB (issue #74).
const (
	DefaultMaxBytes         int64 = 5 << 20
	DefaultDocumentMaxBytes int64 = 40 << 20
	DefaultTimeout                = 20 * time.Second
	DefaultMaxRedirects           = 5
	dialTimeout                   = 10 * time.Second
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

	// documentMaxBytes is the ceiling for a response isDocument accepts, and
	// isDocument is the caller's rule rather than this package's: what counts as
	// a document is the extractor's business, and one predicate is better than
	// two that drift. It is given the first bytes as well as the header, because
	// a mislabelled document is only identifiable by its magic number. A nil
	// isDocument means one cap for everything.
	documentMaxBytes int64
	isDocument       func(contentType string, head []byte) bool

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

// WithDocumentCap raises the cap for the responses f accepts to
// DefaultDocumentMaxBytes, or to n when n is positive. f sees the head of the
// body as well as the content type, so a PDF served as application/octet-stream
// gets the document cap rather than being refused at the page cap before
// anything could tell what it is (issue #74).
func WithDocumentCap(f func(contentType string, head []byte) bool, n ...int64) Option {
	return func(c *Client) {
		if f == nil {
			return
		}
		c.isDocument = f
		c.documentMaxBytes = DefaultDocumentMaxBytes
		if len(n) > 0 && n[0] > 0 {
			c.documentMaxBytes = n[0]
		}
	}
}

// capFor is the byte ceiling for one response, from its content type and the
// first bytes of its body.
func (c *Client) capFor(contentType string, head []byte) int64 {
	if c.isDocument != nil && c.isDocument(contentType, head) {
		return c.documentMaxBytes
	}
	return c.maxBytes
}

// headBytes is how much of a body is read before the cap is chosen. It is the
// length of the longest magic number Harbor checks, and reading it costs nothing
// that is not written to the temp file anyway.
const headBytes = 5

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
	// A PDF is asked for explicitly, because a save that points at one should get
	// the document rather than a preview page or a 406 (issue #74).
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/pdf,text/plain;q=0.8,*/*;q=0.1")
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
	// The cap depends on what answered, and a mislabelled document can only be
	// told from its first bytes: a PDF served as application/octet-stream is
	// still a paper, and it must not be refused at the page cap before the sniff
	// runs (issue #74, found in review). So the head is read first — it is
	// written to the temp file regardless — and the limit is chosen after it.
	head := make([]byte, headBytes)
	n, headErr := io.ReadFull(resp.Body, head)
	if headErr != nil && !errors.Is(headErr, io.ErrUnexpectedEOF) && !errors.Is(headErr, io.EOF) {
		return nil, fmt.Errorf("fetch: reading the head: %w", headErr)
	}
	head = head[:n]
	limit := c.capFor(resp.Header.Get("Content-Type"), head)
	if resp.ContentLength > limit {
		return nil, fmt.Errorf("%w: %d bytes declared, cap %d", ErrTooLarge, resp.ContentLength, limit)
	}

	tmp, err := os.CreateTemp("", "harbor-fetch-*")
	if err != nil {
		return nil, fmt.Errorf("fetch: temp file: %w", err)
	}
	if _, writeErr := tmp.Write(head); writeErr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("fetch: temp file: %w", writeErr)
	}
	// One byte past the cap distinguishes "at the cap" from "over it".
	written, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, limit-int64(len(head))+1))
	written += int64(len(head))
	if copyErr == nil && written > limit {
		copyErr = fmt.Errorf("%w: more than %d bytes", ErrTooLarge, limit)
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
