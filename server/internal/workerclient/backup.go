package workerclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Backups are the one artefact that must leave the potato (design.md §4). The
// potato holds no R2 credential, so this route is the only way a snapshot gets
// off the box that holds the library.

// maxBackupAttempts matches the retry patience of a nightly job: three tries over
// about a minute, then the failure is reported rather than swallowed.
const maxBackupAttempts = 3

// Uploads get their own patience. The client's DefaultTimeout bounds a queue
// call, but it also covers writing a request body — and a 100 MB snapshot over a
// home uplink takes minutes, not thirty seconds. A slow-but-working upload must
// not be turned into three timed-out attempts every night.
const (
	minUploadTimeout = 5 * time.Minute
	// uploadFloorBytesPerSecond is a deliberately pessimistic link: a backup that
	// is still moving at 50 KiB/s is still worth waiting for.
	uploadFloorBytesPerSecond = 50 * 1024
)

// uploadTimeout scales with the body, with a floor for small files and the
// client's own bound above it.
func uploadTimeout(size int64) time.Duration {
	seconds := size / uploadFloorBytesPerSecond
	scaled := time.Duration(seconds) * time.Second
	if scaled < minUploadTimeout {
		return minUploadTimeout
	}
	return scaled
}

// BackupSummary is one snapshot on the Worker: the listing the retention
// reconciliation needs.
type BackupSummary struct {
	Name     string `json:"name"`
	Bytes    int64  `json:"bytes"`
	Uploaded int64  `json:"uploaded"`
}

// PutBackup uploads one compressed snapshot. body must be seekable: a retry
// re-reads it rather than buffering a database in memory.
//
// size is the exact byte count (the Worker requires a fixed length, and R2 will
// not take an unbounded stream); sha256 is the hex digest the Worker records so a
// restored file can be checked against what was uploaded.
func (c *Client) PutBackup(ctx context.Context, name string, body io.ReadSeeker, size int64, sha256 string, schema int) error {
	if size <= 0 {
		return errors.New("workerclient: a backup must have a size")
	}
	path := "/backups/" + url.PathEscape(name)

	var lastErr error
	for attempt := 1; attempt <= maxBackupAttempts; attempt++ {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("workerclient: rewinding the backup: %w", err)
		}
		err := c.putBackupOnce(ctx, path, body, size, sha256, schema)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryableBackup(err) || attempt == maxBackupAttempts {
			break
		}
		wait := time.Duration(attempt) * 2 * time.Second
		var apiErr *Error
		if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
			wait = apiErr.RetryAfter
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return lastErr
}

func (c *Client) putBackupOnce(ctx context.Context, path string, body io.Reader, size int64, sha256 string, schema int) error {
	// A separate client: same transport and proxies, patience scaled to the body.
	uploader := &http.Client{Transport: c.http.Transport, Timeout: uploadTimeout(size)}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+path, body)
	if err != nil {
		return fmt.Errorf("workerclient: %w", err)
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/zstd")
	req.Header.Set("X-Harbor-Sha256", sha256)
	req.Header.Set("X-Harbor-Schema", strconv.Itoa(schema))
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := uploader.Do(req)
	if err != nil {
		return fmt.Errorf("workerclient: %s: %w", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("workerclient: reading %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp, data)
	}
	return nil
}

// ListBackups reports what the Worker holds. Reconciliation uses it: the remote
// side is derived from what is local *now*, not from the names this run happened
// to prune, so a night with no --upload cannot leak an R2 copy forever.
func (c *Client) ListBackups(ctx context.Context) ([]BackupSummary, error) {
	data, err := c.do(ctx, http.MethodGet, "/backups", nil, nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Backups []BackupSummary `json:"backups"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("workerclient: decode /backups: %w", err)
	}
	return body.Backups, nil
}

// DeleteBackup removes one snapshot from R2. Retention calls it after pruning the
// local copy, and a missing object is not an error: the goal is "it is gone".
func (c *Client) DeleteBackup(ctx context.Context, name string) error {
	err := c.doDelete(ctx, "/backups/"+url.PathEscape(name))
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *Client) doDelete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+path, nil)
	if err != nil {
		return fmt.Errorf("workerclient: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("workerclient: %s: %w", path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("workerclient: reading %s: %w", path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp, data)
	}
	return nil
}

// retryableBackup is the nightly job's patience: a Worker that is down, restarting
// or rate limiting is worth another try; a rejected body is not.
func retryableBackup(err error) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 500 || apiErr.Status == http.StatusTooManyRequests
	}
	// A transport failure (no response at all) is worth retrying.
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}
