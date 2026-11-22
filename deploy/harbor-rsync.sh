#!/usr/bin/env bash
#
# Copy the potato's library to a machine that is not the potato (plans/12a).
#
# This runs on the machine that *receives* the copy — the MacBook Air, typically
# from a launchd or cron job — and pulls over ssh. The distroless image has no
# rsync and no shell, so the copy cannot be pushed from inside the container; the
# potato side needs nothing but the ssh server it already runs.
#
#   ./deploy/harbor-rsync.sh                          # pull into ~/Backups/Harbor
#   ./deploy/harbor-rsync.sh --from potato-remote     # another ssh host alias
#   ./deploy/harbor-rsync.sh --to /Volumes/Backup/Harbor
#   ./deploy/harbor-rsync.sh --media                  # media too (can be large)
#   ./deploy/harbor-rsync.sh --prune                  # mirror, deleting extras
#   ./deploy/harbor-rsync.sh --dry-run
#
# Exit codes: 0 copied (or nothing to do), 1 a copy failed, 2 usage.
# An unreachable potato is 0 with a note: a laptop that is asleep is not an error,
# and a nightly job that screams when the network is down trains people to ignore
# it. A partial copy is also fine — rsync resumes — but the state file records it.

set -uo pipefail

HOST=${HARBOR_RSYNC_HOST:-potato-remote}
REMOTE=${HARBOR_RSYNC_REMOTE:-Harbor/data}
DEST=${HARBOR_RSYNC_DEST:-$HOME/Backups/Harbor}
MEDIA=0
PRUNE=0
DRY_RUN=0
CONNECT_TIMEOUT=${HARBOR_RSYNC_TIMEOUT:-8}

usage() {
	sed -n '3,22p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
	--from) HOST=${2:-}; shift 2 ;;
	--to) DEST=${2:-}; shift 2 ;;
	--remote) REMOTE=${2:-}; shift 2 ;;
	--media) MEDIA=1; shift ;;
	--prune) PRUNE=1; shift ;;
	--dry-run) DRY_RUN=1; shift ;;
	-h | --help) usage ;;
	*) echo "harbor-rsync: unknown argument $1" >&2; usage ;;
	esac
done

[ -n "$HOST" ] || { echo "harbor-rsync: no source host" >&2; exit 2; }
[ -n "$DEST" ] || { echo "harbor-rsync: no destination" >&2; exit 2; }

STATE_DIR="$DEST"
mkdir -p "$STATE_DIR" || { echo "harbor-rsync: cannot create $STATE_DIR" >&2; exit 1; }

status() { printf '%-10s %s\n' "$1" "$2"; }

# Write the record the operator (and, when the copy is pushed from the other side,
# the Status page) reads. JSON because something other than a human will read it.
record() {
	local outcome=$1 detail=$2 files bytes at
	at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	files=$(find "$DEST" -type f ! -name '.last-rsync.json' 2>/dev/null | wc -l | tr -d ' ')
	bytes=$(du -sk "$DEST" 2>/dev/null | awk '{print $1 * 1024}')
	printf '{"at":"%s","host":"%s","outcome":"%s","detail":"%s","files":%s,"bytes":%s}\n' \
		"$at" "$HOST" "$outcome" "$detail" "${files:-0}" "${bytes:-0}" >"$DEST/.last-rsync.json"
}

# Reachability first: a copy that hangs for two minutes on a sleeping laptop is
# worse than a copy that decides not to run.
if ! ssh -o BatchMode=yes -o ConnectTimeout="$CONNECT_TIMEOUT" "$HOST" true 2>/dev/null; then
	status "unreachable" "$HOST did not answer within ${CONNECT_TIMEOUT}s; nothing copied"
	record "unreachable" "$HOST did not answer"
	exit 0
fi

RSYNC_ARGS=(-az --partial --timeout=120 --human-readable)
[ "$PRUNE" = "1" ] && RSYNC_ARGS+=(--delete)
[ "$DRY_RUN" = "1" ] && RSYNC_ARGS+=(--dry-run --itemize-changes)

failed=0

# The backups are the copy that matters: uncompressed pre-update snapshots and the
# nightly .db.zst files both live here.
status "backups" "$HOST:$REMOTE/backups -> $DEST/backups"
rsync "${RSYNC_ARGS[@]}" "$HOST:$REMOTE/backups/" "$DEST/backups/" || failed=1

# Media is optional and can be the bulk of the library.
if [ "$MEDIA" = "1" ] && [ "$failed" = "0" ]; then
	status "media" "$HOST:$REMOTE/media -> $DEST/media"
	rsync "${RSYNC_ARGS[@]}" "$HOST:$REMOTE/media/" "$DEST/media/" || failed=1
fi

if [ "$DRY_RUN" = "1" ]; then
	status "dry-run" "nothing was written"
	exit 0
fi

if [ "$failed" = "0" ]; then
	status "copied" "$DEST"
	record "ok" "copied from $HOST"
	exit 0
fi

status "failed" "rsync reported an error; see the output above"
record "failed" "rsync reported an error"
exit 1
