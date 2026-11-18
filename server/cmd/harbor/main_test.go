package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AutumnsGrove/Harbor/server/internal/build"
)

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	if err := run(context.Background(), []string{"version"}, env(map[string]string{}), &stdout); err != nil {
		t.Fatalf("run(version) error = %v", err)
	}
	if got, want := strings.TrimSpace(stdout.String()), build.Version(); got != want {
		t.Errorf("run(version) printed %q, want %q", got, want)
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var stdout bytes.Buffer
		if err := run(context.Background(), []string{arg}, env(map[string]string{}), &stdout); err != nil {
			t.Fatalf("run(%s) error = %v", arg, err)
		}
		if !strings.Contains(stdout.String(), "Usage: harbor") {
			t.Errorf("run(%s) printed %q, want the usage text", arg, stdout.String())
		}
	}
}

func TestRunRejects(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{name: "no command", args: nil, want: "no command given"},
		{name: "an unknown command", args: []string{"frobnicate"}, want: "unknown command"},
		{
			name: "serve with an unusable environment",
			args: []string{"serve"},
			env:  map[string]string{build.EnvPort: "eighty"},
			want: build.EnvPort,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout bytes.Buffer
			err := run(context.Background(), test.args, env(test.env), &stdout)
			if err == nil {
				t.Fatal("run() error = nil, want a rejection")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("run() error = %q, want it to mention %q", err, test.want)
			}
		})
	}
}

func TestProbe(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{name: "healthy", status: http.StatusOK},
		{name: "unhealthy", status: http.StatusServiceUnavailable, wantErr: true},
		{name: "unauthorized", status: http.StatusForbidden, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{"status":"ok","version":"dev"}`))
			}))
			defer server.Close()

			err := probe(server.URL, server.Client())
			if test.wantErr && err == nil {
				t.Fatal("probe() error = nil, want a failure")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("probe() error = %v, want nil", err)
			}
		})
	}
}

func TestProbeUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	if err := probe(url, &http.Client{}); err == nil {
		t.Fatal("probe() against a closed server error = nil, want a failure")
	}
}

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}
