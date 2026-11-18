#!/usr/bin/env bash
# The ten-minute tick: a heartbeat, and a look for a signal the path unit missed.
#
# It never updates on its own. A signal file is the only reason it starts the
# update service; if the file is not there, nothing happens (design.md §3).
set -euo pipefail

ROOT="${HARBOR_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
SIGNAL_DIR="${HARBOR_SIGNAL_DIR:-$ROOT/update-signal}"
SYSTEMCTL="${SYSTEMCTL:-systemctl}"

log() { printf '%s harbor-tick: %s\n' "$(date -u +%FT%TZ)" "$*"; }

mkdir -p "$SIGNAL_DIR"
date -u +%FT%TZ >"$SIGNAL_DIR/heartbeat"

if [ -f "$SIGNAL_DIR/check" ] || [ -f "$SIGNAL_DIR/requested" ]; then
	log "a signal is waiting; starting harbor-update.service"
	"$SYSTEMCTL" start harbor-update.service
fi
