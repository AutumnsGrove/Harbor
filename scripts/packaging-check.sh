#!/usr/bin/env bash
# Packaging rules that a human can break by accident but CI must not accept.
# Every check here is a rule from docs/plans/00-foundations.md or design.md §3.
#
# Runs in `make check` and in CI. It needs no Docker: the image itself is checked
# by `docker-check` (scripts/image-check.sh) in the publish job, where an image
# exists.
set -euo pipefail
cd "$(dirname "$0")/.."

failures=0
pass() { printf '  ok   %s\n' "$1"; }
fail() { printf '  FAIL %s\n' "$1" >&2; failures=$((failures + 1)); }

checks=0
expect() { checks=$((checks + 1)); }

# ── nothing built is committed ──────────────────────────────────────────────
expect
tracked=$(git ls-files web/build)
if [ "$tracked" = "web/build/.gitkeep" ]; then
	pass "web/build holds exactly .gitkeep"
else
	fail "web/build tracks: ${tracked:-<nothing>} (want web/build/.gitkeep only)"
fi

expect
if [ -z "$(git ls-files bin/) " ] || [ -z "$(git ls-files bin 2>/dev/null)" ]; then
	pass "bin/ is not tracked"
else
	fail "bin/ is tracked; the binary lives only inside the image"
fi

# ── the image context is source only ────────────────────────────────────────
for excluded in '.git' 'web/build' 'web/node_modules' 'data/' 'update-signal/' '.env'; do
	expect
	if grep -qx -- "$excluded" .dockerignore 2>/dev/null || grep -q -- "^${excluded%/}$" .dockerignore 2>/dev/null; then
		pass ".dockerignore excludes $excluded"
	else
		fail ".dockerignore does not exclude $excluded"
	fi
done

# ── the runtime image is small, non-root and labelled ───────────────────────
runtime=$(sed -n '/^FROM .*distroless/,$p' Dockerfile)
expect
if grep -q '^USER 1000:1000' <<<"$runtime"; then
	pass "the image runs as uid 1000"
else
	fail "the runtime stage does not set USER 1000:1000"
fi

expect
if grep -q 'org.opencontainers.image.source="https://github.com/AutumnsGrove/Harbor"' Dockerfile; then
	pass "the image carries the source label (GHCR links it to the repo)"
else
	fail "the image is missing org.opencontainers.image.source"
fi

expect
if grep -q 'HEALTHCHECK' Dockerfile; then
	pass "the image defines a HEALTHCHECK"
else
	fail "the image defines no HEALTHCHECK"
fi

expect
if ! grep -Eq '^(FROM|COPY|ADD|RUN).*(npm|pnpm|node)' <<<"$runtime"; then
	pass "the runtime stage installs no Node tooling"
else
	fail "the runtime stage mentions node/npm/pnpm"
fi

# ── compose keeps the container contained ───────────────────────────────────
for setting in 'read_only: true' 'cap_drop: \[ALL\]' "no-new-privileges:true" 'tmpfs:' ; do
	expect
	if grep -Eq -- "$setting" docker-compose.yml; then
		pass "compose sets ${setting%%:*}"
	else
		fail "compose is missing $setting"
	fi
done

expect
if grep -q "127.0.0.1:\${HARBOR_HOST_PORT:-8897}:8080" docker-compose.yml; then
	pass "compose publishes on loopback only, 8897 -> 8080"
else
	fail "compose does not publish 127.0.0.1:\${HARBOR_HOST_PORT:-8897}:8080"
fi

expect
if grep -q "test: \['CMD', '/app/harbor', 'healthcheck'\]" docker-compose.yml; then
	pass "compose health-checks with the binary (distroless has no curl)"
else
	fail "compose healthcheck does not call /app/harbor healthcheck"
fi

# ── the port has one meaning in each place ──────────────────────────────────
expect
if grep -q '^HARBOR_HOST_PORT=8897' .env.example && ! grep -q '^HARBOR_PORT=' .env.example; then
	pass ".env.example carries HARBOR_HOST_PORT and leaves HARBOR_PORT to the binary"
else
	fail ".env.example must set HARBOR_HOST_PORT=8897 and must not set HARBOR_PORT"
fi

# ── no script may invoke a system command by a bare helper name ─────────────
# `say` is macOS speech synthesis. A shell helper with that name is one deleted
# function definition away from reading test output out loud.
expect
if ! grep -rn '^[[:space:]]*say[[:space:]]' scripts deploy --include="*.sh" >/dev/null 2>&1; then
	pass "no shell script invokes the macOS say command"
else
	fail "a shell script calls 'say', which speaks on macOS"
fi

# ── CSP stays strict ────────────────────────────────────────────────────────
expect
if ! grep -rEq "(script-src|style-src)[^\"']*unsafe-inline" server/internal/httpapi web/src; then
	pass "no unsafe-inline anywhere in the CSP or the shell"
else
	fail "unsafe-inline appears in the server or the frontend"
fi

# ── a pull request can never publish ────────────────────────────────────────
# The publish job is the one job that can put an untested image where the potato
# will pull it, so the gate is asserted here rather than trusted.
ci=".github/workflows/ci.yml"
publish_block="$(awk '/^  publish:/{flag=1} flag' "$ci" 2>/dev/null || printf '')"

expect
if [ -n "$publish_block" ] && printf '%s' "$publish_block" | grep -q 'refs/heads/main'; then
	pass "the publish job is gated on main"
else
	fail "the publish job is not gated on a push to main"
fi

expect
needs_line="$(printf '%s' "$publish_block" | grep -m1 '^    needs:')"
missing=""
for job in check fuzz web watcher packaging image; do
	if ! printf '%s' "$needs_line" | grep -qE "(^|[^a-z-])${job}([^a-z-]|$)"; then
		missing="$missing $job"
	fi
done
if [ -z "$missing" ]; then
	pass "the publish job needs every test job"
else
	fail "the publish job does not need:$missing"
fi

# ── the built frontend has no off-host addresses ────────────────────────────
# Warbler's rule, kept: nothing in the shell may fetch from another origin. The
# check is on HTML/CSS references, not on URLs inside error strings.
if [ -d web/build ] && [ -f web/build/index.html ]; then
	expect
	offhost=$(grep -rEoh '(src|href)="https?://[^"]+|url\((["'"'"']?)https?://[^)]+' web/build --include="*.html" --include="*.css" || true)
	if [ -z "$offhost" ]; then
		pass "the built shell references no off-host address"
	else
		fail "the built shell references off-host addresses: $offhost"
	fi
fi

echo
if [ "$failures" -ne 0 ]; then
	echo "packaging: $failures of $checks checks failed" >&2
	exit 1
fi
echo "packaging: $checks checks passed"
