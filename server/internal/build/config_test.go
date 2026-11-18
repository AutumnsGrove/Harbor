package build

import (
	"reflect"
	"strings"
	"testing"
)

// env is a getenv stand-in backed by a map, so tests never touch the process
// environment or each other.
func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// dockerEnv is the smallest .env that is valid in docker mode. Tests copy it and
// break one value at a time.
func dockerEnv() map[string]string {
	return map[string]string{
		EnvDeployment:   "docker",
		EnvDataDir:      "/data",
		EnvUpdateDir:    "/update-signal",
		EnvAllowedLogin: "autumn@grove.place",
		EnvAllowedHosts: "harbor.tailnet.ts.net,localhost,127.0.0.1",
	}
}

func TestLoadValid(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Config
	}{
		{
			name: "docker with an explicit contract",
			env:  dockerEnv(),
			want: Config{
				Deployment:   DeploymentDocker,
				Port:         defaultPort,
				DataDir:      "/data",
				UpdateDir:    "/update-signal",
				AllowedLogin: "autumn@grove.place",
				AllowedHosts: []string{"harbor.tailnet.ts.net", "localhost", "127.0.0.1"},
				UID:          defaultUID,
				GID:          defaultGID,
			},
		},
		{
			name: "docker reads the uid and gid the compose file passes",
			env: merged(dockerEnv(), map[string]string{
				EnvUID: "0",
				EnvGID: "1001",
			}),
			want: Config{
				Deployment:   DeploymentDocker,
				Port:         defaultPort,
				DataDir:      "/data",
				UpdateDir:    "/update-signal",
				AllowedLogin: "autumn@grove.place",
				AllowedHosts: []string{"harbor.tailnet.ts.net", "localhost", "127.0.0.1"},
				UID:          0,
				GID:          1001,
			},
		},
		{
			name: "dev gets relative paths and localhost hosts",
			env: map[string]string{
				EnvDevNoAuth: "1",
			},
			want: Config{
				Deployment:   DeploymentDev,
				Port:         defaultPort,
				DataDir:      "data",
				UpdateDir:    "update-signal",
				AllowedHosts: []string{"localhost", "127.0.0.1", "[::1]"},
				UID:          defaultUID,
				GID:          defaultGID,
				DevNoAuth:    true,
			},
		},
		{
			name: "surrounding whitespace and case are forgiven",
			env: merged(dockerEnv(), map[string]string{
				EnvDeployment:   " Docker ",
				EnvPort:         " 9090 ",
				EnvAllowedLogin: "  Autumn@Grove.Place  ",
				EnvAllowedHosts: " Harbor.Tailnet.TS.NET , localhost ,, 127.0.0.1 , harbor.tailnet.ts.net ",
			}),
			want: Config{
				Deployment:   DeploymentDocker,
				Port:         9090,
				DataDir:      "/data",
				UpdateDir:    "/update-signal",
				AllowedLogin: "Autumn@Grove.Place",
				AllowedHosts: []string{"harbor.tailnet.ts.net", "localhost", "127.0.0.1"},
				UID:          defaultUID,
				GID:          defaultGID,
			},
		},
		{
			name: "the worker and nova settings are carried, not interpreted",
			env: merged(dockerEnv(), map[string]string{
				EnvWorkerURL:        "https://harbor-worker.example.workers.dev",
				EnvWorkerToken:      "door-token",
				EnvOpenRouterAPIKey: "sk-or-test",
			}),
			want: Config{
				Deployment:       DeploymentDocker,
				Port:             defaultPort,
				DataDir:          "/data",
				UpdateDir:        "/update-signal",
				AllowedLogin:     "autumn@grove.place",
				AllowedHosts:     []string{"harbor.tailnet.ts.net", "localhost", "127.0.0.1"},
				UID:              defaultUID,
				GID:              defaultGID,
				WorkerURL:        "https://harbor-worker.example.workers.dev",
				WorkerToken:      "door-token",
				OpenRouterAPIKey: "sk-or-test",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Load(env(test.env))
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("Load() = %#v\nwant   %#v", got, test.want)
			}
		})
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{
			name: "docker without the data directory",
			env:  without(dockerEnv(), EnvDataDir),
			want: []string{EnvDataDir, "required"},
		},
		{
			name: "docker with a relative data directory",
			env:  merged(dockerEnv(), map[string]string{EnvDataDir: "./data"}),
			want: []string{EnvDataDir, "absolute"},
		},
		{
			name: "docker without the update directory",
			env:  without(dockerEnv(), EnvUpdateDir),
			want: []string{EnvUpdateDir, "required"},
		},
		{
			name: "docker without a login",
			env:  without(dockerEnv(), EnvAllowedLogin),
			want: []string{EnvAllowedLogin, "required"},
		},
		{
			name: "docker without a host allowlist",
			env:  without(dockerEnv(), EnvAllowedHosts),
			want: []string{EnvAllowedHosts, "required"},
		},
		{
			name: "dev without a login",
			env:  map[string]string{},
			want: []string{EnvAllowedLogin, "required"},
		},
		{
			name: "the dev auth bypass on the potato",
			env:  merged(dockerEnv(), map[string]string{EnvDevNoAuth: "true"}),
			want: []string{EnvDevNoAuth, "must not be set"},
		},
		{
			name: "an unknown deployment",
			env:  merged(dockerEnv(), map[string]string{EnvDeployment: "kubernetes"}),
			want: []string{EnvDeployment, "unknown deployment"},
		},
		{
			name: "a port that is not a number",
			env:  merged(dockerEnv(), map[string]string{EnvPort: "eighty"}),
			want: []string{EnvPort, "not a number"},
		},
		{
			name: "a port out of range",
			env:  merged(dockerEnv(), map[string]string{EnvPort: "70000"}),
			want: []string{EnvPort, "out of range"},
		},
		{
			name: "port zero",
			env:  merged(dockerEnv(), map[string]string{EnvPort: "0"}),
			want: []string{EnvPort, "out of range"},
		},
		{
			name: "a negative uid",
			env:  merged(dockerEnv(), map[string]string{EnvUID: "-1"}),
			want: []string{EnvUID, "out of range"},
		},
		{
			name: "docker without the loopback the healthcheck uses",
			env:  merged(dockerEnv(), map[string]string{EnvAllowedHosts: "harbor.tailnet.ts.net,localhost"}),
			want: []string{EnvAllowedHosts, "127.0.0.1"},
		},
		{
			name: "a host allowlist entry with a scheme",
			env:  merged(dockerEnv(), map[string]string{EnvAllowedHosts: "https://harbor.tailnet.ts.net"}),
			want: []string{EnvAllowedHosts, "is a URL"},
		},
		{
			name: "a host allowlist entry with a port",
			env:  merged(dockerEnv(), map[string]string{EnvAllowedHosts: "localhost:8897"}),
			want: []string{EnvAllowedHosts, "host:port"},
		},
		{
			name: "a login that is a display name",
			env:  merged(dockerEnv(), map[string]string{EnvAllowedLogin: "Autumn Brown"}),
			want: []string{EnvAllowedLogin, "not a Tailscale login"},
		},
		{
			name: "every problem is reported at once",
			env: map[string]string{
				EnvDeployment:   "docker",
				EnvAllowedLogin: "autumn@grove.place",
			},
			want: []string{EnvDataDir, EnvUpdateDir, EnvAllowedHosts},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(env(test.env))
			if err == nil {
				t.Fatal("Load() error = nil, want a rejection")
			}
			for _, want := range test.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Load() error = %q, want it to mention %q", err, want)
				}
			}
		})
	}
}

func TestHealthURL(t *testing.T) {
	tests := []struct {
		name    string
		port    string
		want    string
		wantErr bool
	}{
		{name: "default port", want: "http://127.0.0.1:8080/healthz"},
		{name: "the port the container publishes", port: "8897", want: "http://127.0.0.1:8897/healthz"},
		{name: "a bad port is not guessed", port: "eighty", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := HealthURL(env(map[string]string{EnvPort: test.port}))
			if test.wantErr {
				if err == nil {
					t.Fatalf("HealthURL(%q) error = nil, want a rejection", test.port)
				}
				return
			}
			if err != nil {
				t.Fatalf("HealthURL(%q) error = %v", test.port, err)
			}
			if got != test.want {
				t.Errorf("HealthURL(%q) = %q, want %q", test.port, got, test.want)
			}
		})
	}
}

func TestParseBool(t *testing.T) {
	trueCases := []string{"1", "true", "TRUE", " yes ", "on"}
	falseCases := []string{"", "0", "false", "no", "off"}

	for _, raw := range trueCases {
		got, err := parseBool(raw)
		if err != nil || !got {
			t.Errorf("parseBool(%q) = %v, %v; want true, nil", raw, got, err)
		}
	}
	for _, raw := range falseCases {
		got, err := parseBool(raw)
		if err != nil || got {
			t.Errorf("parseBool(%q) = %v, %v; want false, nil", raw, got, err)
		}
	}
	if _, err := parseBool("maybe"); err == nil {
		t.Error("parseBool(\"maybe\") error = nil, want a rejection")
	}
}

func merged(base, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overrides))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overrides {
		out[key] = value
	}
	return out
}

func without(base map[string]string, key string) map[string]string {
	out := merged(base, nil)
	delete(out, key)
	return out
}
