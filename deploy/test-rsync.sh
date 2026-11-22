#!/usr/bin/env bash
#
# Tests for deploy/harbor-rsync.sh, with ssh and rsync stubbed out.
#
# The script's interesting behaviour is not the copy itself (rsync does that and
# is not ours to test) but its decisions: do not run when the potato is asleep, do
# not report success when the copy failed, and always leave a record a human or a
# status page can read.

set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SCRIPT="$ROOT/deploy/harbor-rsync.sh"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/harbor-rsync-test.XXXXXX")
FAILURES=0
CHECKS=0

cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

ok() { CHECKS=$((CHECKS + 1)); printf '  ok   %s\n' "$*"; }
no() { CHECKS=$((CHECKS + 1)); FAILURES=$((FAILURES + 1)); printf '  FAIL %s\n' "$*"; }

# ── stubs ───────────────────────────────────────────────────────────────────

BIN="$WORK/bin"
mkdir -p "$BIN"

# ssh succeeds or fails depending on a marker file, and records that it ran.
cat >"$BIN/ssh" <<'STUB'
#!/usr/bin/env bash
echo "ssh $*" >>"${STUB_LOG:?}"
[ -f "${STUB_UNREACHABLE:-/nonexistent}" ] && exit 255
exit 0
STUB

# rsync records its arguments and creates a file, so "copied" means copyable.
cat >"$BIN/rsync" <<'STUB'
#!/usr/bin/env bash
echo "rsync $*" >>"${STUB_LOG:?}"
[ -f "${STUB_RSYNC_FAILS:-/nonexistent}" ] && exit 23
dest="${@: -1}"
mkdir -p "$dest"
: >"$dest/from-the-potato.db"
exit 0
STUB
chmod +x "$BIN/ssh" "$BIN/rsync"
export PATH="$BIN:$PATH"
export STUB_LOG="$WORK/calls.log"
: >"$STUB_LOG"

run_rsync() {
	"$SCRIPT" --to "$1" "${@:2}" >>"$WORK/out.log" 2>&1
}

# ── cases ───────────────────────────────────────────────────────────────────

printf '\nharbor-rsync\n'

dest="$WORK/copy"
if run_rsync "$dest"; then ok "a reachable potato copies"; else no "a reachable potato copies"; fi
grep -q 'ssh -o BatchMode=yes' "$STUB_LOG" && ok "reachability is checked before copying" || no "reachability is checked before copying"
grep -q 'rsync -az --partial' "$STUB_LOG" && ok "rsync runs with the resume-friendly flags" || no "rsync runs with the resume-friendly flags"
[ -f "$dest/backups/from-the-potato.db" ] && ok "the copy landed in backups/" || no "the copy landed in backups/"

record="$dest/.last-rsync.json"
if [ -f "$record" ]; then
	ok "a record is written"
	if grep -q '"outcome":"ok"' "$record"; then ok "the record says ok"; else no "the record says ok"; fi
	if python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$record" 2>/dev/null; then
		ok "the record is valid JSON"
	else
		no "the record is valid JSON"
	fi
else
	no "a record is written"
fi

# An asleep potato is not an error: the job must exit 0 and say so.
: >"$STUB_LOG"
touch "$WORK/unreachable"
if STUB_UNREACHABLE="$WORK/unreachable" run_rsync "$WORK/copy2"; then
	ok "an unreachable host is not a failure"
else
	no "an unreachable host is not a failure"
fi
grep -q 'rsync' "$STUB_LOG" && no "nothing is copied when the host is unreachable" || ok "nothing is copied when the host is unreachable"
grep -q '"outcome":"unreachable"' "$WORK/copy2/.last-rsync.json" 2>/dev/null && ok "the record says unreachable" || no "the record says unreachable"

# A failed copy must not pretend to have worked.
: >"$STUB_LOG"
touch "$WORK/rsync-fails"
if STUB_RSYNC_FAILS="$WORK/rsync-fails" run_rsync "$WORK/copy3"; then
	no "a failed rsync exits non-zero"
else
	ok "a failed rsync exits non-zero"
fi
grep -q '"outcome":"failed"' "$WORK/copy3/.last-rsync.json" 2>/dev/null && ok "the record says failed" || no "the record says failed"

# --dry-run writes nothing.
: >"$STUB_LOG"
run_rsync "$WORK/copy4" --dry-run
grep -q -- '--dry-run' "$STUB_LOG" && ok "--dry-run passes through to rsync" || no "--dry-run passes through to rsync"
grep -q '"outcome"' "$WORK/copy4/.last-rsync.json" 2>/dev/null && no "--dry-run writes no record" || ok "--dry-run writes no record"

# --prune is opt-in, and only then does rsync delete.
: >"$STUB_LOG"
run_rsync "$WORK/copy5" --prune
grep -q -- '--delete' "$STUB_LOG" && ok "--prune asks rsync to mirror" || no "--prune asks rsync to mirror"

# --media adds the second copy.
: >"$STUB_LOG"
run_rsync "$WORK/copy6" --media
media_calls=$(grep -c 'Harbor/data/media' "$STUB_LOG")
[ "$media_calls" = "1" ] && ok "--media copies the media directory" || no "--media copies the media directory"

printf '\nrsync: %d checks, %d failed\n' "$CHECKS" "$FAILURES"
[ "$FAILURES" -eq 0 ]
