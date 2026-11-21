#!/usr/bin/env bash
# Inspect a built Harbor image. The static rules live in packaging-check.sh; this
# one needs the artifact, so it runs where an image exists (CI publish job, or
# locally after `make docker`).
#
# It asserts the things a distroless image can still get wrong:
#   - it runs as uid 1000, not root;
#   - there is no Node toolchain inside it (the frontend is embedded);
#   - /healthz answers from a real container whose data volume uid 1000 owns;
#   - the container's own HEALTHCHECK reports healthy.
set -euo pipefail

image="${1:?usage: $0 <image>}"

fail() {
	echo "image-check: $1" >&2
	exit 1
}

workdir=$(mktemp -d)
container="harbor-image-check-$$"
port="${HARBOR_IMAGE_CHECK_PORT:-18897}"
cleanup() {
	docker rm -f "$container" >/dev/null 2>&1 || true
	rm -rf "$workdir"
}
trap cleanup EXIT

echo "image-check: $image"

# ── identity ────────────────────────────────────────────────────────────────
user=$(docker inspect --format '{{.Config.User}}' "$image")
[ "$user" = "1000:1000" ] || fail "image runs as '$user', want '1000:1000'"

# ── no Node runtime ────────────────────────────────────────────────────────
created=$(docker create --entrypoint /app/harbor "$image" version)
docker export "$created" | tar -t >"$workdir/files.txt" || fail "could not read the image filesystem"
docker rm "$created" >/dev/null
if grep -Eq '(^|/)(node|nodejs|npm|npx|pnpm|yarn)$' "$workdir/files.txt"; then
	fail "the image contains a Node toolchain: $(grep -E '(^|/)(node|nodejs|npm|npx|pnpm|yarn)$' "$workdir/files.txt" | head -3 | tr '\n' ' ')"
fi

# ── it actually serves ─────────────────────────────────────────────────────
mkdir -p "$workdir/data" "$workdir/update-signal"
# The container runs as uid 1000 and the store proves it can write to its data
# dir before it listens, so a volume owned by the runner's uid would fail boot.
# Production gets this from deploy/install.sh, which chowns the data dir.
chmod 0777 "$workdir/data" "$workdir/update-signal"
docker run -d --name "$container" \
	--read-only \
	--tmpfs /tmp \
	-p "127.0.0.1:${port}:8080" \
	-e HARBOR_DEPLOYMENT=dev \
	-e HARBOR_DEV_NO_AUTH=1 \
	-e HARBOR_DATA_DIR=/data \
	-e HARBOR_UPDATE_DIR=/update-signal \
	-v "$workdir/data:/data" \
	-v "$workdir/update-signal:/update-signal" \
	"$image" >/dev/null

healthy=""
for _ in $(seq 1 40); do
	if body=$(curl -fsS "http://127.0.0.1:${port}/healthz" 2>/dev/null); then
		healthy=$body
		break
	fi
	sleep 0.5
done
[ -n "$healthy" ] || {
	docker logs "$container" >&2 || true
	fail "/healthz did not answer inside 20s"
}
echo "$healthy" | grep -q '"status":"ok"' || fail "unexpected /healthz body: $healthy"

# The image's own HEALTHCHECK must agree with the curl the test just made.
for _ in $(seq 1 20); do
	status=$(docker inspect --format '{{.State.Health.Status}}' "$container" 2>/dev/null || echo unknown)
	[ "$status" = "healthy" ] && break
	sleep 1
done
[ "$status" = "healthy" ] || fail "container health is '$status', want 'healthy'"

echo "image-check: ok (uid 1000, no Node, /healthz healthy, HEALTHCHECK healthy)"
