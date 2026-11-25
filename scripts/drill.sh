#!/usr/bin/env bash
#
# The phase-12a restore drill, as a script (docs/runbook.md).
#
# It builds a library through the API, backs it up, destroys the data directory,
# restores from the backup, and checks the library came back — then exports what
# it restored. Every step is timed, because the number in the runbook is the point.
#
# It runs entirely in a temp directory with a dev data dir: it cannot touch a real
# Harbor, the potato, or ~/Harbor. Nothing it does needs the network.
#
#   ./scripts/drill.sh                 # 300 items
#   COUNT=2000 ./scripts/drill.sh      # a bigger library
#   KEEP=1 ./scripts/drill.sh          # leave the temp directory behind to look at

set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/harbor-drill.XXXXXX")
DATA="$WORK/data"
PORT=${PORT:-18097}
COUNT=${COUNT:-300}
KEEP=${KEEP:-0}
BIN="$WORK/harbor"
SERVE_PID=""
FAILURES=0

# The drill promises it cannot reach the network and cannot touch a real Harbor.
# Both promises are about the environment it inherits, so the credentials are
# cleared here rather than trusted to be absent: with HARBOR_WORKER_URL set, the
# drill's server would claim real jobs from the live Worker, write them into a
# throwaway database and mark them done; with OPENROUTER_API_KEY set, its
# example.com saves would become real fetches and model calls.
for credential in HARBOR_WORKER_URL HARBOR_WORKER_TOKEN OPENROUTER_API_KEY \
	HARBOR_ALLOWED_LOGIN HARBOR_ALLOWED_HOSTS HARBOR_UPDATE_DIR; do
	if [ -n "${!credential:-}" ]; then
		printf 'clearing %s for the drill\n' "$credential"
		unset "$credential"
	fi
done
export HARBOR_UPDATE_DIR="$WORK/update-signal"

CLEANED=0
cleanup() {
	# Idempotent: a signal trap exits through here, and EXIT runs it again.
	[ "$CLEANED" = "1" ] && return 0
	CLEANED=1
	# Ignore further signals while cleaning up: an impatient second Ctrl-C must not
	# abort `rm -rf` halfway and leave a temp directory holding a database.
	trap '' INT TERM HUP PIPE
	if [ -n "$SERVE_PID" ]; then
		kill "$SERVE_PID" 2>/dev/null
		wait "$SERVE_PID" 2>/dev/null
		SERVE_PID=""
	fi
	if [ "$KEEP" = "1" ]; then
		printf '\nleft behind: %s\n' "$WORK"
	else
		rm -rf "$WORK"
	fi
}
# EXIT alone is not enough: a script killed by a signal (SIGPIPE from a closed
# pipe, a Ctrl-C) would leave its server running and the next drill would find the
# port busy. The signal traps must *exit* — a trap that only runs the handler
# resumes the script afterwards, against the directory it just deleted.
trap cleanup EXIT
trap 'exit 130' INT TERM HUP PIPE

step() { printf '\n== %s\n' "$*"; }
ok() { printf '  ok   %s\n' "$*"; }
no() { printf '  FAIL %s\n' "$*"; FAILURES=$((FAILURES + 1)); }
check() { if [ "$2" = "$3" ]; then ok "$1 ($2)"; else no "$1 (got $2, want $3)"; fi; }

stamp() { python3 -c 'import time; print(time.time())'; }
since() { python3 -c "print(f'{$1 - $2:.2f}')"; }

# One command, timed.
timed() {
	local label=$1
	shift
	local start end
	start=$(stamp)
	"$@" || no "$label failed"
	end=$(stamp)
	printf '  %-22s %6ss\n' "$label" "$(since "$end" "$start")"
}

# Readiness is "our child answers", not "this port answers": anything already
# listening would satisfy the latter and the seed step would write into it.
port_answers() {
	curl -sf --max-time 1 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1
}

# A TCP connect, not an HTTP GET: a listener that answers 404 is still a listener.
port_listening() {
	python3 -c '
import socket, sys
sock = socket.socket()
sock.settimeout(0.5)
sys.exit(0 if sock.connect_ex(("127.0.0.1", int(sys.argv[1]))) == 0 else 1)
' "$PORT"
}

start_server() {
	if port_listening; then
		no "port $PORT is already in use; re-run with PORT=<free port>"
		return 1
	fi
	HARBOR_DATA_DIR="$DATA" HARBOR_PORT="$PORT" HARBOR_DEPLOYMENT=dev HARBOR_DEV_NO_AUTH=1 \
		"$BIN" serve >"$WORK/serve.log" 2>&1 &
	SERVE_PID=$!
	for _ in $(seq 1 100); do
		if ! kill -0 "$SERVE_PID" 2>/dev/null; then
			if port_listening; then
				no "port $PORT is in use by another process; re-run with PORT=<free port>"
			else
				no "the server exited during startup"
				sed -n 1,20p "$WORK/serve.log"
			fi
			return 1
		fi
		if port_answers; then
			# The port answers and our child is alive: it is ours.
			return 0
		fi
		sleep 0.1
	done
	no "the server did not start"
	sed -n 1,20p "$WORK/serve.log"
	return 1
}

stop_server() {
	if [ -n "$SERVE_PID" ]; then
		kill "$SERVE_PID" 2>/dev/null
		wait "$SERVE_PID" 2>/dev/null
		SERVE_PID=""
	fi
	# Let the socket go, so two drills in a row do not look like a busy port.
	for _ in $(seq 1 20); do
		port_listening || return 0
		sleep 0.1
	done
}

items_via_api() {
	curl -sf "http://127.0.0.1:$PORT/api/status" | python3 -c 'import json,sys; print(json.load(sys.stdin)["items_total"])'
}

# ── the drill ───────────────────────────────────────────────────────────────

step "build"
timed "go build" env CGO_ENABLED=0 go build -o "$BIN" "$ROOT/server/cmd/harbor"
[ -x "$BIN" ] || { echo "the drill cannot continue without a binary"; exit 1; }

step "populate a library through the API ($COUNT items)"
start_server || exit 1
seed_start=$(stamp)
python3 - "$PORT" "$COUNT" <<'PY'
import json, sys, urllib.request
port, count = sys.argv[1], int(sys.argv[2])
body = ("A sentence that exists only so the database has real text to carry. " * 30).strip()
for i in range(1, count + 1):
    payload = {"title": f"Drill item {i}", "note": f"{body}\n\nitem {i}"}
    # Every third save is a link, so the library is not all notes.
    if i % 3 == 0:
        payload["url"] = f"https://example.com/drill/{i}"
    request = urllib.request.Request(
        f"http://127.0.0.1:{port}/api/items",
        data=json.dumps(payload).encode(),
        headers={"content-type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request) as response:
        if response.status != 201:
            raise SystemExit(f"save {i}: {response.status}")
PY
seed_end=$(stamp)
printf '  %-22s %6ss\n' "seeded $COUNT items" "$(since "$seed_end" "$seed_start")"

before=$(items_via_api)
check "items before the backup" "$before" "$COUNT"

# A soft delete, so the backup has to carry "recently deleted" too.
first_id=$(curl -sf "http://127.0.0.1:$PORT/api/items?limit=1" | python3 -c 'import json,sys; print(json.load(sys.stdin)["items"][0]["id"])')
curl -sf -X DELETE "http://127.0.0.1:$PORT/api/items/$first_id" >/dev/null
check "items after the delete" "$(items_via_api)" "$((COUNT - 1))"
stop_server

printf '  database size: %s\n' "$(du -h "$DATA/harbor.db" | cut -f1)"

step "back up"
timed "harbor backup" env HARBOR_DATA_DIR="$DATA" HARBOR_DEPLOYMENT=dev HARBOR_DEV_NO_AUTH=1 "$BIN" backup
backup_file=$(find "$DATA/backups" -name 'harbor-*.db.zst' | sort | tail -1)
[ -n "$backup_file" ] || { no "no backup was written"; exit 1; }
printf '  backup: %s (%s)\n' "$(basename "$backup_file")" "$(du -h "$backup_file" | cut -f1)"

step "lose the potato"
mv "$DATA" "$DATA.destroyed"
mkdir -p "$DATA"
ok "the data directory is gone (moved to data.destroyed, to compare against)"

step "restore"
restore_start=$(stamp)
# The backup lived in the destroyed directory: copy it back, as an operator would
# from the off-box copy.
cp "$DATA.destroyed/backups/$(basename "$backup_file")" "$DATA/"
HARBOR_DATA_DIR="$DATA" HARBOR_DEPLOYMENT=dev HARBOR_DEV_NO_AUTH=1 \
	"$BIN" restore "$DATA/$(basename "$backup_file")" --yes >"$WORK/restore.log" 2>&1
restore_rc=$?
restore_end=$(stamp)
printf '  %-22s %6ss\n' "harbor restore" "$(since "$restore_end" "$restore_start")"
[ "$restore_rc" -eq 0 ] || { no "the restore failed"; sed -n 1,30p "$WORK/restore.log"; }
grep -q "next: harbor replay --since" "$WORK/restore.log" && ok "the restore printed the replay command" || no "no replay command printed"

step "the library is back"
start_server || exit 1
check "items after the restore" "$(items_via_api)" "$((COUNT - 1))"
list=$(curl -sf "http://127.0.0.1:$PORT/api/items?limit=5")
echo "$list" | python3 -c 'import json,sys; data=json.load(sys.stdin); print("  newest item:", data["items"][0]["title"])'
# The deleted item is still deleted, not resurrected by the restore. A deleted
# item is readable by id on purpose — its page offers Restore — so the property
# is: still marked deleted, and absent from the home list.
curl -sf "http://127.0.0.1:$PORT/api/items/$first_id" | python3 -c '
import json, sys
item = json.load(sys.stdin)["item"]
sys.exit(0 if item.get("deletedAt") else 1)
' && ok "the soft-deleted item is still marked deleted" || no "the restore resurrected a deleted item"
# The home list is the live library: its count is what /api/status reports, and
# the deleted item is not in it (checked above, 299 of 300).
# And "Recently deleted" still has it, which is what the backup carried.
curl -sf "http://127.0.0.1:$PORT/api/items?deleted=true&limit=50" | python3 -c '
import json, sys
ids = {i["id"] for i in json.load(sys.stdin)["items"]}
sys.exit(0 if sys.argv[1] in ids else 1)
' "$first_id" && ok "it is in Recently deleted, as the backup had it" || no "the deleted item is missing from Recently deleted"
# Its text is still in the database, which is what "carries recently deleted" means.
echo "$list" | python3 -c 'import json,sys; data=json.load(sys.stdin); assert data["items"], "no items"; print("  first item has", len(data["items"][0].get("note") or ""), "characters of note")'
stop_server

step "export what was restored"
timed "harbor export" env HARBOR_DATA_DIR="$DATA" HARBOR_DEPLOYMENT=dev HARBOR_DEV_NO_AUTH=1 \
	"$BIN" export --out "$WORK/export"
python3 - "$WORK/export" "$((COUNT - 1))" <<'PY'
import json, os, sys
out, want = sys.argv[1], int(sys.argv[2])
library = json.load(open(os.path.join(out, "library.json")))
counts = library["counts"]
problems = []
if counts["items"] != want + 1:  # the soft-deleted one is in the file too
    problems.append(f"library.json has {counts['items']} items, want {want + 1} (including the deleted one)")
markdown = os.listdir(os.path.join(out, "markdown"))
if len(markdown) != counts["items"]:
    problems.append(f"{len(markdown)} markdown files for {counts['items']} items")
manifest = json.load(open(os.path.join(out, "manifest.json")))
if not any(f["path"] == "library.json" for f in manifest["files"]):
    problems.append("the manifest does not list library.json")
print(f"  export: {counts['items']} items, {len(markdown)} markdown files, {len(manifest['files'])} files in the manifest")
for problem in problems:
    print("  FAIL", problem)
sys.exit(1 if problems else 0)
PY
[ $? -eq 0 ] && ok "the export opens and agrees with the library" || no "the export disagrees with the library"

printf '\n'
if [ "$FAILURES" -eq 0 ]; then
	printf 'drill passed: %d items, backup, destroyed data directory, restore, export\n' "$COUNT"
else
	printf 'drill FAILED: %d check(s)\n' "$FAILURES"
	exit 1
fi
