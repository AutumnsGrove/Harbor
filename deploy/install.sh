#!/usr/bin/env bash
# Install Harbor on the potato. Idempotent: run it again after a `git pull` and
# it reinstalls the units and leaves the data alone.
#
#   cd ~/Harbor && sudo deploy/install.sh
#
# It does not build anything and it does not expose the tailnet. The image comes
# from GHCR, and `tailscale serve` is a one-time command printed at the end
# (design.md §3: document it, do it by hand once).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT/.env"
SYSTEMD_DIR="${SYSTEMD_DIR:-/etc/systemd/system}"
UNITS=(harbor-update.service harbor-update.path harbor-tick.service harbor-tick.timer harbor-firewall.service)

log() { printf 'install: %s\n' "$*"; }
die() {
	printf 'install: error: %s\n' "$*" >&2
	exit 1
}
warn() { printf 'install: warning: %s\n' "$*" >&2; }

[ "$(id -u)" -eq 0 ] || die "run with sudo: sudo $0"

for tool in docker systemctl ss curl sed install; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

read_env() {
	local key=$1 line
	while IFS= read -r line; do
		case "$line" in
		"$key="*) printf '%s' "${line#*=}" && return 0 ;;
		esac
	done <"$ENV_FILE"
	return 1
}

# ── 1. the environment file ─────────────────────────────────────────────────

if [ ! -f "$ENV_FILE" ]; then
	install -m 600 "$ROOT/.env.example" "$ENV_FILE"
	log "created .env from .env.example (mode 600)"
fi
chmod 600 "$ENV_FILE"

login="$(read_env HARBOR_ALLOWED_LOGIN || true)"
case "$login" in
"" | "you@example.com")
	die ".env still has the placeholder HARBOR_ALLOWED_LOGIN; set your Tailscale login first"
	;;
esac

hosts="$(read_env HARBOR_ALLOWED_HOSTS || true)"
case "$hosts" in
*"<"* | "") die ".env HARBOR_ALLOWED_HOSTS is empty or still a placeholder" ;;
esac

image="$(read_env HARBOR_IMAGE || true)"
case "$image" in
*"@"sha256:*) : ;;
*) log "HARBOR_IMAGE is a tag; the first update will pin a digest" ;;
esac

uid="$(read_env HARBOR_UID || true)"
gid="$(read_env HARBOR_GID || true)"
uid="${uid:-1000}"
gid="${gid:-1000}"

# ── 2. directories ──────────────────────────────────────────────────────────

install -d -m 755 "$ROOT/data" "$ROOT/data/backups" "$ROOT/update-signal"
chown -R "$uid:$gid" "$ROOT/data"
chown "$uid:$gid" "$ROOT/update-signal"
log "data and update-signal are owned by $uid:$gid"

# ── 3. the port is free ─────────────────────────────────────────────────────

port="$(read_env HARBOR_HOST_PORT || true)"
port="${port:-8897}"
if ss -ltn "sport = :$port" 2>/dev/null | grep -q LISTEN; then
	owner="$(docker ps --filter "publish=$port" --format '{{.Names}}' 2>/dev/null | head -1 || true)"
	case "$owner" in
	harbor-harbor-1 | harbor-harbor) log "port $port is already Harbor's own container" ;;
	*) die "port $port is in use${owner:+ by $owner}; pick a free HARBOR_HOST_PORT (docs/meridian.md)" ;;
	esac
fi

# ── 4. units ────────────────────────────────────────────────────────────────

for unit in "${UNITS[@]}"; do
	sed "s|@ROOT@|$ROOT|g" "$ROOT/deploy/$unit" >"$SYSTEMD_DIR/$unit"
	chmod 644 "$SYSTEMD_DIR/$unit"
	log "installed $SYSTEMD_DIR/$unit"
done
systemctl daemon-reload

systemctl enable harbor-firewall.service >/dev/null
systemctl enable --now harbor-update.path >/dev/null
systemctl enable --now harbor-tick.timer >/dev/null
log "enabled update path, tick timer and firewall unit"

# ── 5. first run ────────────────────────────────────────────────────────────

log "pulling $image"
docker compose --project-directory "$ROOT" --env-file "$ENV_FILE" pull

log "starting"
docker compose --project-directory "$ROOT" --env-file "$ENV_FILE" up -d

log "waiting for /healthz up to 120s"
healthy=0
for _ in $(seq 1 240); do
	if curl -fsS --max-time 2 "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
		healthy=1
		break
	fi
	sleep 0.5
done
if [ "$healthy" -ne 1 ]; then
	docker compose --project-directory "$ROOT" --env-file "$ENV_FILE" logs --tail 40 harbor >&2 || true
	die "Harbor did not become healthy; see the logs above and docs/update.md"
fi

# The egress network exists only once compose has created it, so the firewall is
# applied last (and again at every boot by the unit).
systemctl restart harbor-firewall.service
log "egress rules applied"

# ── 6. what is left, by hand ────────────────────────────────────────────────

version="$(curl -fsS "http://127.0.0.1:$port/healthz" || true)"
log "healthy: $version"
cat <<EOF

Harbor is running on 127.0.0.1:$port. Two things are deliberately manual:

  1. Expose it on the tailnet, once:
       sudo tailscale serve --bg --https=$port http://127.0.0.1:$port

  2. Add the Meridian tile, allowlist target and observed unit:
       deploy/meridian/tile.yaml
       deploy/meridian/targets.json
       deploy/meridian/observe.json
     explained in docs/meridian.md — not before /healthz answers, which it now does.

Updates: Settings → Check, or \`sudo systemctl start harbor-update.service\`.
docs/update.md is the page to read when something goes wrong.
EOF
