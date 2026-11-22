package workerclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The backup upload is the potato's only outbound write that is not a queue
// operation, and it carries the irreplaceable artefact. What matters: the right
// headers, the exact bytes, and a retry that re-reads rather than truncates.

type backupServer struct {
	mu       sync.Mutex
	requests []*capturedRequest
	statuses []int
}

type capturedRequest struct {
	method      string
	path        string
	contentType string
	sha256      string
	schema      string
	auth        string
	length      int64
	body        []byte
}

func (b *backupServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAllLimited(r.Body)
		b.mu.Lock()
		b.requests = append(b.requests, &capturedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			contentType: r.Header.Get("Content-Type"),
			sha256:      r.Header.Get("X-Harbor-Sha256"),
			schema:      r.Header.Get("X-Harbor-Schema"),
			auth:        r.Header.Get("Authorization"),
			length:      r.ContentLength,
			body:        body,
		})
		status := http.StatusCreated
		if len(b.statuses) > 0 {
			status = b.statuses[0]
			b.statuses = b.statuses[1:]
		}
		b.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		if status >= 300 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"nope"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"name":"x","bytes":10}`))
	})
}

func readAllLimited(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(readerFunc(func(p []byte) (int, error) { return r.Read(p) }))
	return buf.Bytes(), err
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestPutBackupSendsTheBytesAndTheHeaders(t *testing.T) {
	server := &backupServer{}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()

	payload := []byte("zstd-bytes-of-a-database")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])

	client := newTestClient(t, ts.URL)
	if err := client.PutBackup(context.Background(), "harbor-20261122T180000Z.db.zst",
		bytes.NewReader(payload), int64(len(payload)), digest, 1); err != nil {
		t.Fatalf("PutBackup: %v", err)
	}

	if len(server.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(server.requests))
	}
	got := server.requests[0]
	if got.method != http.MethodPut || got.path != "/backups/harbor-20261122T180000Z.db.zst" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.contentType != "application/zstd" || got.sha256 != digest || got.schema != "1" {
		t.Errorf("headers = %+v", got)
	}
	if got.auth != "Bearer potato-token" {
		t.Errorf("auth = %q", got.auth)
	}
	if got.length != int64(len(payload)) || !bytes.Equal(got.body, payload) {
		t.Errorf("body = %d bytes (content-length %d), want %d", len(got.body), got.length, len(payload))
	}
}

func TestPutBackupRetriesAWorkerThatIsDown(t *testing.T) {
	server := &backupServer{statuses: []int{http.StatusServiceUnavailable, http.StatusCreated}}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()

	client := newTestClient(t, ts.URL)
	payload := []byte("payload")
	if err := client.PutBackup(context.Background(), "harbor-20261122T180000Z.db.zst",
		bytes.NewReader(payload), int64(len(payload)), strings.Repeat("a", 64), 1); err != nil {
		t.Fatalf("PutBackup: %v", err)
	}
	if len(server.requests) != 2 {
		t.Fatalf("attempts = %d, want a retry then success", len(server.requests))
	}
	// The retry must send the whole body again, not the remainder.
	if !bytes.Equal(server.requests[1].body, payload) {
		t.Errorf("second attempt sent %q, want the whole payload", server.requests[1].body)
	}
}

func TestPutBackupDoesNotRetryARejectedBody(t *testing.T) {
	server := &backupServer{statuses: []int{http.StatusBadRequest}}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()

	client := newTestClient(t, ts.URL)
	err := client.PutBackup(context.Background(), "harbor-20261122T180000Z.db.zst",
		bytes.NewReader([]byte("x")), 1, strings.Repeat("a", 64), 1)
	if err == nil {
		t.Fatal("a 400 should fail the upload")
	}
	if len(server.requests) != 1 {
		t.Errorf("attempts = %d, want one: a rejected body is not retried", len(server.requests))
	}
}

func TestPutBackupNeedsASize(t *testing.T) {
	client := newTestClient(t, "http://127.0.0.1:1")
	if err := client.PutBackup(context.Background(), "x.db.zst", bytes.NewReader(nil), 0, "a", 1); err == nil {
		t.Fatal("a zero-size backup should be refused before the request")
	}
}

func TestDeleteBackupTreatsMissingAsDone(t *testing.T) {
	server := &backupServer{statuses: []int{http.StatusNotFound}}
	ts := httptest.NewServer(server.handler())
	defer ts.Close()

	client := newTestClient(t, ts.URL)
	if err := client.DeleteBackup(context.Background(), "harbor-20261122T180000Z.db.zst"); err != nil {
		t.Fatalf("DeleteBackup on a missing object: %v", err)
	}
	if len(server.requests) != 1 || server.requests[0].method != http.MethodDelete {
		t.Errorf("requests = %+v", server.requests)
	}
}
