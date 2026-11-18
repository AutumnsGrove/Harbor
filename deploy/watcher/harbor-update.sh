#!/usr/bin/env bash
# Harbor's update watcher: the one thing on the potato allowed to change the
# running image (design.md §3).
#
# Three ways in, one script:
#
#   update-signal/check      → resolve :latest, write available.json, change nothing
#   update-signal/requested  → an update was asked for from Settings
#   bare systemctl start     → an update was asked for by Meridian or by hand
#
# An update is: snapshot, pin the digest, pull, recreate, health-check, and roll
# back if the new version is not healthy. The app never names an image; this
# script owns the repository and accepts only <repo>@sha256:<64 hex>.
set -euo pipefail

ROOT="${HARBOR_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
ENV_FILE="${HARBOR_ENV_FILE:-$ROOT/.env}"
SIGNAL_DIR="${HARBOR_SIGNAL_DIR:-$ROOT/update-signal}"
DATA_DIR="${HARBOR_DATA_DIR:-$ROOT/data}"
SNAPSHOT="$DATA_DIR/backups/pre-update.db"

DOCKER="${DOCKER:-docker}"
CURL="${CURL:-curl}"

HEALTH_TIMEOUT="${HARBOR_HEALTH_TIMEOUT:-120}"
HEALTH_INTERVAL="${HARBOR_HEALTH_INTERVAL:-0.5}"

CHECK_FILE="$SIGNAL_DIR/check"
REQUEST_FILE="$SIGNAL_DIR/requested"
AVAILABLE_FILE="$SIGNAL_DIR/available.json"
STATUS_FILE="$SIGNAL_DIR/status.json"
HEARTBEAT_FILE="$SIGNAL_DIR/heartbeat"
LOCK_DIR="$SIGNAL_DIR/.lock.d"

log() { printf '%s harbor-update: %s\n' "$(date -u +%FT%TZ)" "$*"; }
die() {
	log "error: $*"
	exit 1
}

# ── signal files ────────────────────────────────────────────────────────────

# status writes one JSON object atomically. The settings page reads this file, so
# it is never left half-written.
status() {
	local state=$1 message=${2:-} at
	at="$(date -u +%FT%TZ)"
	printf '{"status":"%s","message":"%s","at":"%s"}\n' \
		"$state" "${message//\"/}" "$at" >"$STATUS_FILE.tmp"
	mv "$STATUS_FILE.tmp" "$STATUS_FILE"
	log "status: $state${message:+ ($message)}"
}

available() {
	local digest=$1 repo=$2 desired
	desired="$(read_env_value HARBOR_IMAGE || true)"
	if [ "$desired" = "$repo@$digest" ]; then
		printf '{"available":false,"digest":"%s","repo":"%s","at":"%s"}\n' \
			"$digest" "$repo" "$(date -u +%FT%TZ)" >"$AVAILABLE_FILE.tmp"
	else
		printf '{"available":true,"digest":"%s","repo":"%s","at":"%s"}\n' \
			"$digest" "$repo" "$(date -u +%FT%TZ)" >"$AVAILABLE_FILE.tmp"
	fi
	mv "$AVAILABLE_FILE.tmp" "$AVAILABLE_FILE"
}

# ── .env ────────────────────────────────────────────────────────────────────

# read_env_value reads KEY=value without evaluating the file: .env holds secrets
# and is never sourced.
read_env_value() {
	local key=$1 line
	while IFS= read -r line; do
		case "$line" in
		"$key="*) printf '%s' "${line#*=}" && return 0 ;;
		esac
	done <"$ENV_FILE"
	return 1
}

# rewrite_env_image changes exactly one line. Every other line — comments,
# secrets, blank lines — survives byte-for-byte, which the tests diff for.
rewrite_env_image() {
	local new=$1 tmp
	[ -f "$ENV_FILE" ] || die "no .env at $ENV_FILE"
	tmp="$(mktemp "$ENV_FILE.XXXXXX")"
	awk -v new="$new" '
		BEGIN { found = 0 }
		/^HARBOR_IMAGE=/ { print "HARBOR_IMAGE=" new; found = 1; next }
		{ print }
		END { if (!found) print "HARBOR_IMAGE=" new }
	' "$ENV_FILE" >"$tmp"
	chmod 600 "$tmp"
	mv "$tmp" "$ENV_FILE"
}

# ── image references ────────────────────────────────────────────────────────

is_digest_ref() { [[ $1 =~ ^[^[:space:]]+@sha256:[0-9a-f]{64}$ ]]; }

# repo_of strips the tag or digest, leaving registry/owner/name.
repo_of() {
	local ref=$1
	ref="${ref%%@*}"
	# A colon after the last slash is a tag; a colon before it is a registry port.
	if [[ ${ref##*/} == *:* ]]; then
		ref="${ref%:*}"
	fi
	printf '%s' "$ref"
}

# resolve_digest asks the registry what :latest is right now. The watcher never
# trusts a value from the app or a signal file.
resolve_digest() {
	local repo=$1 digest
	if ! digest="$("$DOCKER" buildx imagetools inspect "$repo:latest" --format '{{.Manifest.Digest}}' 2>/dev/null)"; then
		return 1
	fi
	digest="$(printf '%s' "$digest" | tr -d '[:space:]')"
	[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
	printf '%s' "$digest"
}

compose() { "$DOCKER" compose --project-directory "$ROOT" --env-file "$ENV_FILE" "$@"; }

# ── health ──────────────────────────────────────────────────────────────────

health_port() {
	local port
	port="$(read_env_value HARBOR_HOST_PORT || true)"
	printf '%s' "${port:-8897}"
}

# healthy is the only definition of success the watcher has: the same endpoint
# Docker and Meridian probe.
healthy() {
	"$CURL" -fsS --max-time 2 "http://127.0.0.1:$(health_port)/healthz" >/dev/null 2>&1
}

# wait_healthy polls for HEALTH_TIMEOUT seconds (not iterations: the interval is
# configurable and the budget is the thing the plan promises).
wait_healthy() {
	local deadline
	deadline=$((SECONDS + HEALTH_TIMEOUT))
	while [ "$SECONDS" -lt "$deadline" ]; do
		if healthy; then
			return 0
		fi
		sleep "$HEALTH_INTERVAL"
	done
	return 1
}

# ── snapshot and restore ────────────────────────────────────────────────────

# take_snapshot runs the binary's own snapshot, which is VACUUM INTO plus an
# integrity check and an atomic rename. `compose run` rather than `exec` so it
# works whether or not the container is up.
take_snapshot() {
	log "snapshotting to $SNAPSHOT"
	compose run --rm --no-deps --entrypoint /app/harbor harbor snapshot --pre-update
}

# restore_snapshot is a file-level restore, correct while the database is the only
# state: stop, put the snapshot back, drop any WAL beside the old file.
# Phase 12 replaces this with `harbor restore`, which also knows about media.
restore_snapshot() {
	[ -f "$SNAPSHOT" ] || {
		log "warning: no snapshot at $SNAPSHOT; leaving the database alone"
		return 1
	}
	local uid gid
	log "restoring $SNAPSHOT"
	compose stop harbor || true
	cp "$SNAPSHOT" "$DATA_DIR/harbor.db.tmp"
	mv "$DATA_DIR/harbor.db.tmp" "$DATA_DIR/harbor.db"
	rm -f "$DATA_DIR/harbor.db-wal" "$DATA_DIR/harbor.db-shm"
	uid="$(read_env_value HARBOR_UID || true)"
	gid="$(read_env_value HARBOR_GID || true)"
	chown "${uid:-1000}:${gid:-1000}" "$DATA_DIR/harbor.db" 2>/dev/null || true
}

# ── locking ─────────────────────────────────────────────────────────────────

# The lock is a directory, not flock: macOS has no flock, and the tests have to
# exercise the same path a developer runs. A stale lock (a killed run) is taken
# over rather than left to block every future update — the plan's "a missed
# signal must not leave the app waiting forever" risk.
release_lock() { rm -rf "$LOCK_DIR"; }

acquire_lock() {
	local holder
	if mkdir "$LOCK_DIR" 2>/dev/null; then
		printf '%s\n' "$$" >"$LOCK_DIR/pid"
		trap release_lock EXIT
		trap 'release_lock; exit 130' INT TERM
		return 0
	fi

	holder="$(cat "$LOCK_DIR/pid" 2>/dev/null || true)"
	if [ -n "$holder" ] && kill -0 "$holder" 2>/dev/null; then
		return 1
	fi

	log "taking over a stale lock from ${holder:-an unknown process}"
	rm -rf "$LOCK_DIR"
	if mkdir "$LOCK_DIR" 2>/dev/null; then
		printf '%s\n' "$$" >"$LOCK_DIR/pid"
		trap release_lock EXIT
		trap 'release_lock; exit 130' INT TERM
		return 0
	fi
	return 1
}

# ── the two jobs ────────────────────────────────────────────────────────────

# check only resolves and reports. It must never pull, pin or recreate: the
# settings page's Check button is allowed to be curious without being dangerous.
job_check() {
	local repo digest current
	current="$(read_env_value HARBOR_IMAGE || true)"
	[ -n "$current" ] || die ".env has no HARBOR_IMAGE"
	repo="$(repo_of "$current")"
	rm -f "$CHECK_FILE"
	if ! digest="$(resolve_digest "$repo")"; then
		# "Could not tell" is not "there is something new": an empty digest marked
		# available would make the UI offer an update of nothing.
		printf '{"available":false,"error":"could not resolve %s:latest","at":"%s"}\n' \
			"$repo" "$(date -u +%FT%TZ)" >"$AVAILABLE_FILE.tmp"
		mv "$AVAILABLE_FILE.tmp" "$AVAILABLE_FILE"
		status "failed" "could not resolve $repo:latest"
		exit 1
	fi
	available "$digest" "$repo"
	status "ok" "checked $repo"
}

job_update() {
	local old_ref repo digest new_ref reason

	old_ref="$(read_env_value HARBOR_IMAGE || true)"
	[ -n "$old_ref" ] || die ".env has no HARBOR_IMAGE"
	repo="$(repo_of "$old_ref")"

	if [ -f "$REQUEST_FILE" ]; then
		# The app writes the literal "latest" and nothing else. Anything else is a
		# caller trying to choose the artifact, which is not a thing the app or a
		# browser may do (design.md §3).
		local requested
		requested="$(tr -d '[:space:]' <"$REQUEST_FILE")"
		rm -f "$REQUEST_FILE"
		if [ "$requested" != "latest" ]; then
			status "failed" "refused requested image '$requested'"
			exit 1
		fi
	fi

	if ! digest="$(resolve_digest "$repo")"; then
		status "failed" "could not resolve $repo:latest"
		exit 1
	fi
	new_ref="$repo@$digest"

	if [ "$new_ref" = "$old_ref" ]; then
		# A previous run may have been killed between pinning and recreating: .env
		# and the running container disagree, and every later run would otherwise
		# be a no-op. `up -d` is idempotent and recreates only if the image differs.
		if ! compose up -d harbor; then
			status "failed" "pinned at $digest but the container could not be reconciled"
			exit 1
		fi
		status "ok" "already pinned at $digest"
		exit 0
	fi

	log "updating $old_ref -> $new_ref"

	# Snapshot before pinning, not after: if the snapshot fails, .env still points
	# at the image that is running (design.md §3 puts the pin first; this order is
	# the same guarantee with one less way to be left inconsistent).
	if ! take_snapshot; then
		status "failed" "snapshot failed; not updating"
		exit 1
	fi

	rewrite_env_image "$new_ref"

	if ! compose pull harbor; then
		reason="pull failed"
		rewrite_env_image "$old_ref"
		status "failed" "$reason"
		exit 1
	fi

	if ! compose up -d harbor; then
		reason="recreate failed"
		rewrite_env_image "$old_ref"
		compose up -d harbor || log "warning: recreating the old image also failed"
		status "failed" "$reason"
		exit 1
	fi

	if wait_healthy; then
		status "ok" "updated to $new_ref"
		exit 0
	fi

	log "health check failed within ${HEALTH_TIMEOUT}s; rolling back"
	rewrite_env_image "$old_ref"
	restore_snapshot || true
	if ! compose up -d harbor; then
		log "warning: could not recreate the old image"
	fi
	if wait_healthy; then
		status "failed" "rolled back to $old_ref and healthy"
	else
		status "failed" "rolled back to $old_ref but still unhealthy"
	fi
	exit 1
}

# ── main ────────────────────────────────────────────────────────────────────

main() {
	mkdir -p "$SIGNAL_DIR" "$DATA_DIR"

	# One run at a time: a tick and a path trigger must not both pin and pull.
	if ! acquire_lock; then
		log "another update is running; leaving it alone"
		status "busy" "another update is running"
		exit 0
	fi

	date -u +%FT%TZ >"$HEARTBEAT_FILE"

	if [ -f "$CHECK_FILE" ]; then
		job_check
		return
	fi
	job_update
}

main "$@"
