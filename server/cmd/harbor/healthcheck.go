package main

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

// healthcheck is the container's HEALTHCHECK: distroless has no curl and no
// shell, so the binary asks itself. It exits 0 only on a 200 (design.md §3).
//
// It reads HARBOR_PORT and nothing else on purpose: a broken data path must not
// make a healthy process look dead to Docker, because then the update watcher
// rolls back a fine image.
func healthcheck(getenv func(string) string) error {
	url, err := build.HealthURL(getenv)
	if err != nil {
		return err
	}
	return probe(url, &http.Client{Timeout: 2 * time.Second})
}

func probe(url string, client *http.Client) error {
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("health check: %w", err)
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused; the body carries
	// nothing the check needs.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check: %s answered %s", url, resp.Status)
	}
	return nil
}
