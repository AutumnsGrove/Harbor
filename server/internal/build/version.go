// Package build carries the version stamp and the environment contract of the
// harbor binary. Nothing here touches the network or the filesystem: main wires
// it, and every rule is testable without a container.
package build

import "regexp"

// version is stamped at link time:
//
//	-ldflags "-X github.com/AutumnsGrove/Harbor/server/internal/build.version=r123.abc1234"
//
// A plain `go build`, `go test` or `go run` leaves it empty, which Version
// reports as "dev" rather than as an empty string in a header.
var version string

// versionPattern is the only shape a version may take. It is returned in the
// /healthz JSON body, in /api/version and in the Settings page, so a stamp
// carrying anything outside this set is treated as absent instead of being
// passed through.
var versionPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Version returns the build stamp, or "dev" when the binary was built without
// one. The result always matches ^[A-Za-z0-9._-]{1,64}$ (design.md §3).
func Version() string {
	if versionPattern.MatchString(version) {
		return version
	}
	return "dev"
}
