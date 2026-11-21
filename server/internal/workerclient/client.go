// Package workerclient is the potato's side of the Worker API (design.md §4).
// The potato is always the client: it dials out, never listens for the Worker,
// because a Worker has no route into a tailnet.
//
// Every job is validated against contracts/job.schema.json before it becomes a
// Go value. The schema is the cross-runtime agreement, so a field the Worker
// adds or renames fails here, on the potato, instead of becoming a silently
// zero-valued item. A response the client cannot trust is an error, never a
// partial success.
package workerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/AutumnsGrove/Harbor/contracts"
)

// DefaultTimeout bounds one call. The save path never waits on the Worker, so a
// slow Worker delays the poll, not a save; still, no call is unbounded
// (plans/README.md invariant 7).
const DefaultTimeout = 30 * time.Second

// maxResponseBytes caps a claim response. The Worker limits a claim to 50 jobs,
// so this is generous by an order of magnitude and still finite.
var maxResponseBytes int64 = 8 << 20

// The job values the Worker's schema allows. They are spelled here rather than
// imported from model because this package speaks the wire contract, not the
// database.
const (
	SourceTelegram = "telegram"
	SourceShortcut = "shortcut"

	KindLink   = "link"
	KindNote   = "note"
	KindUpload = "upload"
)

// Job is one claimed job: contracts/job.schema.json.
type Job struct {
	ID        string  `json:"id"`
	Source    string  `json:"source"`
	Kind      string  `json:"kind"`
	Link      *string `json:"link"`
	Note      *string `json:"note"`
	Title     *string `json:"title"`
	GroupKey  *string `json:"group_key"`
	CreatedAt int64   `json:"created_at"`

	Status    string `json:"status,omitempty"`
	Attempts  int    `json:"attempts,omitempty"`
	LastError string `json:"last_error,omitempty"`

	Blobs []Blob `json:"blobs"`
}

// Blob is one file in R2 that belongs to a job. `Bytes` is what the Worker
// wrote; the potato re-verifies size and sha256 when it pulls (S1).
type Blob struct {
	N           int    `json:"n"`
	Key         string `json:"key"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
}

// Error is a non-2xx answer. The Worker's error bodies are `{error: "..."}`;
// anything else is reported with the status alone rather than guessed at.
type Error struct {
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("worker returned %d", e.Status)
	}
	return fmt.Sprintf("worker returned %d: %s", e.Status, e.Message)
}

// ErrUnauthorized matches a 401 or a 403: the token is wrong, revoked, or has
// the wrong scope. The poll loop pauses on it instead of spinning
// (plans/02 test 8).
var ErrUnauthorized = errors.New("workerclient: unauthorized")

// Is lets errors.Is(err, ErrUnauthorized) answer for both statuses without
// callers repeating the pair.
func (e *Error) Is(target error) bool {
	if target == ErrUnauthorized {
		return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
	}
	return false
}

// Client is safe for concurrent use: net/http is, and the fields are read-only
// after New.
type Client struct {
	base      string
	token     string
	userAgent string
	http      *http.Client
}

// New builds a client for baseURL with the potato token. version is the build
// stamp that goes in the User-Agent, so a Worker log line says which potato
// called it.
func New(baseURL, token, version string) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("workerclient: a token is required")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("workerclient: %w", err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("workerclient: %q is not an http(s) URL", baseURL)
	case u.Host == "":
		return nil, fmt.Errorf("workerclient: %q has no host", baseURL)
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("workerclient: %q must not carry a query or fragment", baseURL)
	}
	base := strings.TrimRight(u.String(), "/")

	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	return &Client{
		base:      base,
		token:     token,
		userAgent: "Harbor/" + version + " (+https://github.com/AutumnsGrove/Harbor)",
		http:      &http.Client{Timeout: DefaultTimeout},
	}, nil
}

// WithHTTPClient replaces the default client. Tests use it for a short timeout;
// nothing in production does, because DefaultTimeout is already the cap.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	if h != nil {
		c.http = h
	}
	return c
}

// Claim atomically claims up to limit jobs. kinds restricts the claim to what
// this build can ingest (phase 2 claims link,note; uploads arrive with phase 6),
// so a job the potato cannot process is never leased and then failed.
func (c *Client) Claim(ctx context.Context, limit int, kinds []string) ([]Job, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if len(kinds) > 0 {
		q.Set("kinds", strings.Join(kinds, ","))
	}
	return c.jobs(ctx, http.MethodPost, "/jobs/claim", q)
}

// Pending lists queued and claimed jobs without changing them. It feeds the
// pending cards and never claims (design.md §5).
func (c *Client) Pending(ctx context.Context) ([]Job, error) {
	return c.jobs(ctx, http.MethodGet, "/jobs/pending", nil)
}

// Replay returns the done jobs at or after since. Their blobs are kept by the
// Worker precisely so a restore can re-ingest them (design.md §5).
func (c *Client) Replay(ctx context.Context, since time.Time) ([]Job, error) {
	q := url.Values{}
	q.Set("since", strconv.FormatInt(since.Unix(), 10))
	return c.jobs(ctx, http.MethodGet, "/jobs/replay", q)
}

// Done closes a job. It is idempotent on the Worker, so a retry after a network
// failure is safe.
func (c *Client) Done(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/jobs/"+url.PathEscape(id)+"/done", nil, nil)
	return err
}

// Failed reports a job the potato could not ingest, with a reason that lands in
// the Worker's log and the pending card.
func (c *Client) Failed(ctx context.Context, id, reason string) error {
	body := map[string]string{"error": reason}
	_, err := c.do(ctx, http.MethodPost, "/jobs/"+url.PathEscape(id)+"/failed", nil, body)
	return err
}

// jobs performs a call that answers `{jobs: [...]}` and validates every job.
func (c *Client) jobs(ctx context.Context, method, path string, query url.Values) ([]Job, error) {
	raw, err := c.do(ctx, method, path, query, nil)
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Jobs *[]json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("workerclient: %s answered something that is not a job envelope: %w", path, err)
	}
	if envelope.Jobs == nil {
		return nil, fmt.Errorf("workerclient: %s answered without a jobs array", path)
	}

	jobs := make([]Job, 0, len(*envelope.Jobs))
	for i, raw := range *envelope.Jobs {
		job, err := decodeJob(raw)
		if err != nil {
			return nil, fmt.Errorf("workerclient: %s job %d: %w", path, i, err)
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// decodeJob is the validation boundary: schema first, then the Go type. Doing
// it in this order means an extra or misspelled field is caught by the contract
// rather than dropped by encoding/json.
func decodeJob(raw json.RawMessage) (Job, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return Job{}, fmt.Errorf("not JSON: %w", err)
	}
	if err := contracts.Validate(contracts.Job, doc); err != nil {
		return Job{}, fmt.Errorf("does not match contracts/job.schema.json: %w", err)
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return Job{}, fmt.Errorf("decode: %w", err)
	}
	return job, nil
}

// do is the one HTTP path: bearer token, versioned User-Agent, a byte cap and a
// typed error. No caller reads a response body directly.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	endpoint := c.base + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("workerclient: encode %s: %w", path, err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, payload)
	if err != nil {
		return nil, fmt.Errorf("workerclient: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workerclient: %s: %w", path, err)
	}
	defer resp.Body.Close()

	// One byte over the cap is enough to tell "at the cap" from "past it".
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("workerclient: reading %s: %w", path, err)
	}
	if int64(len(data)) > maxResponseBytes {
		return nil, fmt.Errorf("workerclient: %s answered more than %d bytes", path, maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, apiError(resp, data)
	}
	return data, nil
}

func apiError(resp *http.Response, body []byte) *Error {
	e := &Error{Status: resp.StatusCode}
	var envelope struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		e.Message = envelope.Error
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	}
	return e
}
