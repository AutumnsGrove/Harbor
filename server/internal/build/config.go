package build

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Deployment says where the binary is running. It changes which values have
// defaults, not which checks run: the Host check and the identity lock are the
// same in both modes (design.md §3).
type Deployment string

const (
	// DeploymentDev is a developer's machine. Relative paths are fine and the
	// identity lock may be switched off with HARBOR_DEV_NO_AUTH.
	DeploymentDev Deployment = "dev"
	// DeploymentDocker is the potato. Every path must be absolute, because the
	// root filesystem is read-only, and the identity lock cannot be disabled.
	DeploymentDocker Deployment = "docker"
)

// Environment variable names. They are constants so tests, docs and .env.example
// cannot drift from the parser.
const (
	EnvDeployment       = "HARBOR_DEPLOYMENT"
	EnvPort             = "HARBOR_PORT"
	EnvDataDir          = "HARBOR_DATA_DIR"
	EnvUpdateDir        = "HARBOR_UPDATE_DIR"
	EnvAllowedLogin     = "HARBOR_ALLOWED_LOGIN"
	EnvAllowedHosts     = "HARBOR_ALLOWED_HOSTS"
	EnvUID              = "HARBOR_UID"
	EnvGID              = "HARBOR_GID"
	EnvDevNoAuth        = "HARBOR_DEV_NO_AUTH"
	EnvWorkerURL        = "HARBOR_WORKER_URL"
	EnvWorkerToken      = "HARBOR_WORKER_TOKEN"
	EnvOpenRouterAPIKey = "OPENROUTER_API_KEY"
)

// Dev defaults. They are the only defaults: docker mode insists on being told
// every path and host, which is what makes a misconfigured potato fail at boot
// instead of at the first request.
const (
	defaultPort = 8080
	defaultUID  = 1000
	defaultGID  = 1000

	devDataDir      = "./data"
	devUpdateDir    = "./update-signal"
	devAllowedHosts = "localhost,127.0.0.1,[::1]"
)

// Config is the whole environment contract of the binary (design.md §3). Load
// resolves every default and rejects every value the rest of the program would
// otherwise have to defend against.
type Config struct {
	Deployment   Deployment
	Port         int
	DataDir      string
	UpdateDir    string
	AllowedLogin string
	AllowedHosts []string
	UID          int
	GID          int

	// DevNoAuth turns the identity lock off. It exists so `make dev` does not
	// need a Tailscale header; Load refuses it in docker mode.
	DevNoAuth bool

	// Parsed now, used from phase 1 (Worker) and phase 3 (Nova). They are
	// checked for shape only, so a typo in .env fails at boot rather than at the
	// first save that needs them.
	WorkerURL        string
	WorkerToken      string
	OpenRouterAPIKey string
}

// Load builds a Config from getenv: os.Getenv in main, a map lookup in tests.
// Every problem is reported at once, because a container that refuses to boot
// should not make the operator find its mistakes one restart at a time.
func Load(getenv func(string) string) (Config, error) {
	var problems []error
	reject := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}

	deployment, err := parseDeployment(getenv(EnvDeployment))
	if err != nil {
		reject("%v", err)
	}

	port, err := parseInt(getenv(EnvPort), defaultPort, 1, 65535)
	if err != nil {
		reject("%s: %v", EnvPort, err)
	}
	uid, err := parseInt(getenv(EnvUID), defaultUID, 0, 1<<31-1)
	if err != nil {
		reject("%s: %v", EnvUID, err)
	}
	gid, err := parseInt(getenv(EnvGID), defaultGID, 0, 1<<31-1)
	if err != nil {
		reject("%s: %v", EnvGID, err)
	}
	devNoAuth, err := parseBool(getenv(EnvDevNoAuth))
	if err != nil {
		reject("%s: %v", EnvDevNoAuth, err)
	}

	dataDir, err := requirePath(getenv(EnvDataDir), devDataDir, deployment, EnvDataDir)
	if err != nil {
		reject("%v", err)
	}
	updateDir, err := requirePath(getenv(EnvUpdateDir), devUpdateDir, deployment, EnvUpdateDir)
	if err != nil {
		reject("%v", err)
	}

	login := strings.TrimSpace(getenv(EnvAllowedLogin))
	if login == "" && !devNoAuth {
		reject("%s is required: the one Tailscale login allowed to write (or set %s=1 for local dev)", EnvAllowedLogin, EnvDevNoAuth)
	}
	if login != "" && !looksLikeLogin(login) {
		reject("%s %q is not a Tailscale login (expected name@domain)", EnvAllowedLogin, login)
	}
	if devNoAuth && deployment == DeploymentDocker {
		reject("%s must not be set when %s=%s: the potato is never unauthenticated", EnvDevNoAuth, EnvDeployment, DeploymentDocker)
	}

	hosts, err := parseHosts(getenv(EnvAllowedHosts), deployment)
	if err != nil {
		reject("%s: %v", EnvAllowedHosts, err)
	}

	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}

	return Config{
		Deployment:       deployment,
		Port:             port,
		DataDir:          filepath.Clean(dataDir),
		UpdateDir:        filepath.Clean(updateDir),
		AllowedLogin:     login,
		AllowedHosts:     hosts,
		UID:              uid,
		GID:              gid,
		DevNoAuth:        devNoAuth,
		WorkerURL:        strings.TrimSpace(getenv(EnvWorkerURL)),
		WorkerToken:      strings.TrimSpace(getenv(EnvWorkerToken)),
		OpenRouterAPIKey: strings.TrimSpace(getenv(EnvOpenRouterAPIKey)),
	}, nil
}

// parseDeployment accepts the two modes design.md §3 defines. An unset value is
// dev; a misspelled one is an error, never a silent fallback.
func parseDeployment(raw string) (Deployment, error) {
	switch Deployment(strings.ToLower(strings.TrimSpace(raw))) {
	case DeploymentDev:
		return DeploymentDev, nil
	case DeploymentDocker:
		return DeploymentDocker, nil
	case "":
		return DeploymentDev, nil
	default:
		return "", fmt.Errorf("%s: unknown deployment %q (want %q or %q)", EnvDeployment, raw, DeploymentDev, DeploymentDocker)
	}
}

// requirePath applies the dev default when the variable is unset, and requires
// an absolute path in docker, where "/" is read-only and a relative path would
// resolve somewhere unwritable.
func requirePath(raw, devDefault string, deployment Deployment, name string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		if deployment == DeploymentDocker {
			return "", fmt.Errorf("%s is required in %s mode", name, deployment)
		}
		value = devDefault
	}
	if deployment == DeploymentDocker && !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s %q must be absolute in %s mode: the root filesystem is read-only", name, value, deployment)
	}
	return value, nil
}

// looksLikeLogin checks the shape of a Tailscale identity: a name, one @, and a
// domain. The value is compared exactly, so this only catches a pasted display
// name or a trailing space before it reaches the request path.
func looksLikeLogin(login string) bool {
	name, domain, found := strings.Cut(login, "@")
	return found && name != "" && domain != "" && !strings.ContainsAny(login, " \t/")
}

func parseInt(raw string, fallback, min, max int) (int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", raw)
	}
	if number < min || number > max {
		return 0, fmt.Errorf("%d is out of range [%d, %d]", number, min, max)
	}
	return number, nil
}

// parseBool accepts the spellings an operator actually types in .env. Anything
// else is an error: a typo must not silently disable an auth check.
func parseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, fmt.Errorf("%q is not a boolean (want 0/1, true/false)", raw)
	}
}

// parseHosts lowercases and de-duplicates the Host allowlist, and rejects the
// shapes people paste by mistake (a URL, a path, a host:port pair). Ports are
// not part of the allowlist; the Host check compares hostnames only, because
// `tailscale serve` and the loopback publish use different ports for the same
// site (design.md §3).
func parseHosts(raw string, deployment Deployment) ([]string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if deployment == DeploymentDocker {
			return nil, fmt.Errorf("is required in %s mode: the tailnet hostname and localhost", DeploymentDocker)
		}
		trimmed = devAllowedHosts
	}

	seen := make(map[string]bool)
	hosts := make([]string, 0, 4)
	for _, entry := range strings.Split(trimmed, ",") {
		host := strings.ToLower(strings.TrimSpace(entry))
		if host == "" {
			continue
		}
		switch {
		case strings.Contains(host, "://"):
			return nil, fmt.Errorf("%q is a URL; list the hostname only", entry)
		case strings.ContainsAny(host, "/ \t"):
			return nil, fmt.Errorf("%q is not a hostname", entry)
		case strings.HasPrefix(host, "["):
			// [::1] is the one allowed bracketed form; [::1]:8897 is a mistake.
			if !strings.HasSuffix(host, "]") {
				return nil, fmt.Errorf("%q looks like host:port; list hostnames only", entry)
			}
		case strings.Count(host, ":") > 0:
			return nil, fmt.Errorf("%q looks like host:port; list hostnames only", entry)
		}
		if !seen[host] {
			seen[host] = true
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("lists no hostnames")
	}

	// The container healthcheck probes http://127.0.0.1:<port>/healthz, and the
	// Host check runs on /healthz too. A docker config without 127.0.0.1 boots
	// fine and then answers 421 to every probe, so Docker marks a healthy image
	// unhealthy and the watcher rolls it back. Reject it at boot instead.
	if deployment == DeploymentDocker && !contains(hosts, "127.0.0.1") {
		return nil, fmt.Errorf("must include 127.0.0.1 in %s mode: the container healthcheck probes it, and a 421 there would fail a healthy image", DeploymentDocker)
	}
	return hosts, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
