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
	# shift 2 with one argument left fails and consumes nothing, which would spin
	# this loop forever with no output — in a cron job, that is a wedged timer.
	--from | --to | --remote)
		flag=$1
		if [ $# -lt 2 ] || [ -z "${2:-}" ]; then
			echo "harbor-rsync: $flag needs a value" >&2
			exit 2
		fi
		case "$flag" in
		--from) HOST=$2 ;;
		--to) DEST=$2 ;;
		--remote) REMOTE=$2 ;;
		esac
		shift 2
		;;
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
#
# The record answers two questions, not one: what happened last night, and when was
# the last *good* copy. A single "outcome" field answered only the first, so a
# failed or unreachable night erased the answer to the second — and "copied an hour
# ago, one bad night since" became indistinguishable from "not copied in months".
record() {
	local outcome=$1 detail=$2 at
	at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
	python3 - "$DEST/.last-rsync.json" "$at" "$HOST" "$outcome" "$detail" "$DEST" <<'PY'
import json, os, subprocess, sys

path, at, host, outcome, detail, dest = sys.argv[1:7]

def measure():
    files = bytes_ = 0
    for root, _, names in os.walk(dest):
        for name in names:
            if name == ".last-rsync.json":
                continue
            files += 1
            try:
                bytes_ += os.path.getsize(os.path.join(root, name))
            except OSError:
                pass
    return files, bytes_

previous = {}
if os.path.exists(path):
    try:
        previous = json.load(open(path))
    except (OSError, ValueError):
        previous = {}

record = {
    "at": at,
    "host": host,
    "outcome": outcome,
    "detail": detail,
}
if outcome == "ok":
    files, bytes_ = measure()
    record.update({"last_ok_at": at, "last_ok_host": host, "last_ok_files": files, "last_ok_bytes": bytes_})
else:
    # Carry the last good copy forward: this run did not replace it.
    for key in ("last_ok_at", "last_ok_host", "last_ok_files", "last_ok_bytes"):
        if key in previous:
            record[key] = previous[key]

with open(path, "w") as handle:
    json.dump(record, handle, sort_keys=True)
    handle.write("\n")
PY
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
