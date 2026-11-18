package build

import (
	"strings"
	"testing"
)

// FuzzLoad holds the property the plans/testing.md contract names for config:
// arbitrary HARBOR_PORT and HARBOR_UID strings never panic, and either parse or
// are rejected. A successful parse must also return values the rest of the
// program can rely on without re-checking them.
func FuzzLoad(f *testing.F) {
	f.Add("docker", "8080", "1000", "")
	f.Add("dev", "", "-1", "1")
	f.Add("kubernetes", "eighty", "0x10", "maybe")
	f.Add("", "65535", "4294967296", "true")

	f.Fuzz(func(t *testing.T, deployment, port, uid, devNoAuth string) {
		getenv := func(key string) string {
			switch key {
			case EnvDeployment:
				return deployment
			case EnvPort:
				return port
			case EnvUID:
				return uid
			case EnvDevNoAuth:
				return devNoAuth
			case EnvAllowedLogin:
				return "autumn@grove.place"
			default:
				return ""
			}
		}

		cfg, err := Load(getenv)
		if err != nil {
			return
		}
		if cfg.Port < 1 || cfg.Port > 65535 {
			t.Fatalf("Load() accepted port %d", cfg.Port)
		}
		if cfg.UID < 0 {
			t.Fatalf("Load() accepted uid %d", cfg.UID)
		}
		if cfg.GID < 0 {
			t.Fatalf("Load() accepted gid %d", cfg.GID)
		}
		if len(cfg.AllowedHosts) == 0 {
			t.Fatal("Load() accepted an empty host allowlist")
		}
		if cfg.DevNoAuth && cfg.Deployment == DeploymentDocker {
			t.Fatal("Load() accepted HARBOR_DEV_NO_AUTH in docker mode")
		}
		seen := make(map[string]bool, len(cfg.AllowedHosts))
		for _, host := range cfg.AllowedHosts {
			if host == "" {
				t.Fatal("Load() accepted an empty hostname")
			}
			if host != strings.ToLower(host) {
				t.Fatalf("Load() returned host %q, want lower case", host)
			}
			if seen[host] {
				t.Fatalf("Load() returned host %q twice", host)
			}
			seen[host] = true
		}
	})
}
