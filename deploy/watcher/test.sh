#!/usr/bin/env bash
# Tests for the update watcher. Written before it: these are the failures the
# plan says a rollback path must have actually been through.
#
#   ./deploy/watcher/test.sh          # all cases
#   ./deploy/watcher/test.sh --verbose
#
# Fake `docker` and `curl` live in a temp bin dir and are driven by state files,
# so the cases are deterministic and never touch the network, Docker or the real
# .env. Everything the watcher could get wrong is a file assertion or an exit code.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
WATCHER="$HERE/harbor-update.sh"
TICK="$HERE/harbor-tick.sh"
VERBOSE=0
[ "${1:-}" = "--verbose" ] && VERBOSE=1

pass=0
fail=0
current=""

# Never name a helper `say`: that is macOS's speech command, and a missing
# function definition would have the shell read the test output out loud.
verbose() {
	[ "$VERBOSE" -eq 1 ] && printf '       %s\n' "$*"
	return 0
}

# One line per assertion: the case name is the context, the message is the rule.
ok() {
	pass=$((pass + 1))
	printf '  ok   %s%s\n' "$current" "${1:+ — $1}"
	[ "$VERBOSE" -eq 1 ] && printf '       %s\n' "${1:-$(eval echo "$current")}"
	return 0
}

no() {
	fail=$((fail + 1))
	printf '  FAIL %s — %s\n' "$current" "${1:-assertion}"
	[ -n "${2:-}" ] && printf '       %s\n' "$2"
	return 0
}

assert_eq() {
	local message="${3:-equality}"
	if [ "$1" = "$2" ]; then ok "$message"; else no "$message" "expected [$2], got [$1]"; fi
}

assert_contains() {
	local message="${3:-contains: $2}"
	if printf '%s' "$1" | grep -qF -- "$2"; then ok "$message"; else no "$message" "expected the value to contain [$2]"; fi
}

assert_not_contains() {
	local message="${3:-does not contain: $2}"
	if printf '%s' "$1" | grep -qF -- "$2"; then no "$message" "expected the value not to contain [$2]"; else ok "$message"; fi
}

assert_file() {
	if [ -f "$1" ]; then ok "exists: $1"; else no "missing $1"; fi
}

assert_absent() {
	if [ -e "$1" ]; then no "should not exist: $1"; else ok "absent: $1"; fi
}

# ── fixtures ────────────────────────────────────────────────────────────────

NEW_DIGEST="sha256:$(printf 'b%.0s' $(seq 1 64))"
OLD_DIGEST="sha256:$(printf 'a%.0s' $(seq 1 64))"
REPO="ghcr.io/autumnsgrove/harbor"

setup() {
	WORK="$(mktemp -d)"
	STATE="$WORK/state"
	mkdir -p "$STATE" "$WORK/bin" "$WORK/data/backups" "$WORK/update-signal"

	cat >"$WORK/.env" <<EOF
# Harbor test env
HARBOR_IMAGE=$REPO@$OLD_DIGEST
HARBOR_UID=1000
HARBOR_GID=1000
HARBOR_HOST_PORT=18897
HARBOR_DEPLOYMENT=docker
HARBOR_ALLOWED_LOGIN=autumn@grove.place
OPENROUTER_API_KEY=secret-value-that-must-survive
EOF
	chmod 600 "$WORK/.env"
	cp "$WORK/.env" "$WORK/.env.original"
	printf 'OLD-DB\n' >"$WORK/data/harbor.db"
	touch "$STATE/healthy"

	write_fakes
}

write_fakes() {
	cat >"$WORK/bin/docker" <<'FAKE'
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "$*" >>"$FAKE_STATE/calls"
args=" $* "
case "$args" in
*" snapshots "*)
	exit 99
	;;
esac
if [[ $args == *" buildx imagetools inspect "* ]]; then
	if [ -f "$FAKE_STATE/resolve_fails" ]; then
		exit 1
	fi
	if [ -f "$FAKE_STATE/bad_digest" ]; then
		printf 'sha256:not-a-real-digest\n'
		exit 0
	fi
	printf '%s\n' "$FAKE_STATE_DIGEST"
	exit 0
fi
if [[ $args == *" run "*" snapshot --pre-update "* ]]; then
	if [ -f "$FAKE_STATE/snapshot_fails" ]; then
		exit 1
	fi
	mkdir -p "$FAKE_WORK/data/backups"
	printf 'SNAPSHOT-CONTENT\n' >"$FAKE_WORK/data/backups/pre-update.db"
	exit 0
fi
if [[ $args == *" pull "* ]]; then
	[ -f "$FAKE_STATE/pull_fails" ] && exit 1
	exit 0
fi
if [[ $args == *" up -d "* ]]; then
	[ -f "$FAKE_STATE/up_fails" ] && exit 1
	exit 0
fi
exit 0
FAKE

	cat >"$WORK/bin/curl" <<'FAKE'
#!/usr/bin/env bash
set -uo pipefail
printf '%s\n' "$*" >>"$FAKE_STATE/health_calls"
[ -f "$FAKE_STATE/healthy" ] && exit 0
exit 7
FAKE

	cat >"$WORK/bin/systemctl" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$FAKE_STATE/systemctl_calls"
exit 0
FAKE

	chmod +x "$WORK/bin/docker" "$WORK/bin/curl" "$WORK/bin/systemctl"
}

# run_watcher <timeout-seconds> [args...]
run_watcher() {
	: >"$STATE/calls"
	: >"$STATE/health_calls"
	: >"$STATE/systemctl_calls"
	FAKE_STATE="$STATE" \
		FAKE_WORK="$WORK" \
		FAKE_STATE_DIGEST="$NEW_DIGEST" \
		PATH="$WORK/bin:$PATH" \
		HARBOR_ROOT="$WORK" \
		HARBOR_ENV_FILE="$WORK/.env" \
		HARBOR_SIGNAL_DIR="$WORK/update-signal" \
		HARBOR_DATA_DIR="$WORK/data" \
		HARBOR_HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-3}" \
		HARBOR_HEALTH_INTERVAL=0.05 \
		bash "$WATCHER" "$@" >"$WORK/watcher.log" 2>&1
	WATCHER_EXIT=$?
	verbose "watcher exit $WATCHER_EXIT"
	verbose "$(cat "$WORK/watcher.log")"
	return 0
}

# `stat -c` is GNU, `stat -f` is BSD/macOS; each errors rather than lying on the
# other, so trying GNU first is portable in both directions.
file_mode() {
	if stat -c '%a' "$1" >/dev/null 2>&1; then
		stat -c '%a' "$1"
	else
		stat -f '%Lp' "$1"
	fi
}

image_line() { grep '^HARBOR_IMAGE=' "$WORK/.env"; }
status_json() { cat "$WORK/update-signal/status.json" 2>/dev/null || printf ''; }
calls() { cat "$STATE/calls" 2>/dev/null || printf ''; }
systemctl_calls() { cat "$STATE/systemctl_calls" 2>/dev/null || printf ''; }
health_calls() { wc -l <"$STATE/health_calls" | tr -d ' '; }

teardown() {
	rm -rf "$WORK"
}

# ── cases ───────────────────────────────────────────────────────────────────

case_bare_start_updates_and_pins() {
	current="bare start: resolves, snapshots, pins, pulls, recreates, is healthy"
	setup
	run_watcher

	assert_eq "$WATCHER_EXIT" "0" "exit code"
	assert_eq "$(image_line)" "HARBOR_IMAGE=$REPO@$NEW_DIGEST" "the digest is pinned"

	# Every other line survives byte-for-byte, secrets included.
	local before after
	before="$(grep -v '^HARBOR_IMAGE=' "$WORK/.env.original")"
	after="$(grep -v '^HARBOR_IMAGE=' "$WORK/.env")"
	assert_eq "$after" "$before" "every other .env line is untouched"

	assert_contains "$(calls)" "buildx imagetools inspect $REPO:latest"
	assert_contains "$(calls)" "snapshot --pre-update"
	assert_contains "$(calls)" "pull harbor"
	assert_contains "$(calls)" "up -d harbor"

	# Snapshot before the pin: if the snapshot fails we must not have moved.
	local snapshot_line pull_line
	snapshot_line=$(grep -n "snapshot --pre-update" "$STATE/calls" | head -1 | cut -d: -f1)
	pull_line=$(grep -n "pull harbor" "$STATE/calls" | head -1 | cut -d: -f1)
	if [ "$snapshot_line" -lt "$pull_line" ]; then
		ok "snapshot happens before the pull"
	else
		no "snapshot happens before the pull"
	fi

	assert_contains "$(status_json)" '"status":"ok"'
	assert_eq "$(file_mode "$WORK/.env")" "600" "env stays mode 600"
	teardown
}

case_check_only_changes_nothing() {
	current="check only: writes available.json, no pull, no pin"
	setup
	touch "$WORK/update-signal/check"
	run_watcher

	assert_eq "$WATCHER_EXIT" "0" "exit code"
	assert_absent "$WORK/update-signal/check"
	assert_contains "$(cat "$WORK/update-signal/available.json")" '"available":true'
	assert_contains "$(cat "$WORK/update-signal/available.json")" "$NEW_DIGEST"
	assert_not_contains "$(calls)" "pull"
	assert_not_contains "$(calls)" "up -d"
	assert_eq "$(cat "$WORK/.env")" "$(cat "$WORK/.env.original")" "the env file is byte-identical"
	teardown
}

case_check_reports_up_to_date() {
	current="check only: an unchanged digest reports available:false"
	setup
	printf 'HARBOR_IMAGE=%s@%s\n' "$REPO" "$NEW_DIGEST" >"$WORK/.env.state"
	# Same digest: rewrite .env so the current pin equals what the registry says.
	sed -i.bak "s|^HARBOR_IMAGE=.*|HARBOR_IMAGE=$REPO@$NEW_DIGEST|" "$WORK/.env"
	touch "$WORK/update-signal/check"
	run_watcher

	assert_contains "$(cat "$WORK/update-signal/available.json")" '"available":false'
	assert_eq "$WATCHER_EXIT" "0" "exit code"
	teardown
}

case_second_update_is_a_no_op() {
	current="bare start twice: no second pull, but the container is reconciled"
	setup
	run_watcher
	run_watcher

	assert_eq "$WATCHER_EXIT" "0" "exit code"
	assert_contains "$(status_json)" "already pinned"
	assert_eq "$(grep -c 'pull harbor' "$STATE/calls")" "0" "no pull on the second run"
	# A run killed after pinning but before recreating leaves .env and the
	# container disagreeing; `up -d` is how the next run notices.
	assert_eq "$(grep -c 'up -d harbor' "$STATE/calls")" "1" "the container is reconciled"
	teardown
}

case_reconcile_does_not_report_ok_when_unhealthy() {
	current="reconcile: a killed half-run is recreated and health-checked, not assumed"
	setup
	run_watcher
	# The pin now matches the registry, but the container is not answering.
	rm -f "$STATE/healthy"
	HEALTH_TIMEOUT=1 run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_contains "$(status_json)" "not healthy"
	assert_contains "$(calls)" "up -d harbor"
	teardown
}

case_failed_check_does_not_advertise_an_update() {
	current="a check that cannot resolve says available:false, not an empty digest"
	setup
	touch "$STATE/resolve_fails" "$WORK/update-signal/check"
	run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_contains "$(cat "$WORK/update-signal/available.json")" '"available":false'
	assert_not_contains "$(cat "$WORK/update-signal/available.json")" '"available":true'
	assert_absent "$WORK/update-signal/check"
	teardown
}

case_pull_failure_re_pins_the_old_digest() {
	current="pull failure: the old digest is pinned back, the app is untouched"
	setup
	touch "$STATE/pull_fails"
	run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_eq "$(image_line)" "HARBOR_IMAGE=$REPO@$OLD_DIGEST" "old digest restored"
	assert_contains "$(status_json)" '"status":"failed"'
	assert_not_contains "$(calls)" "up -d"
	assert_eq "$(cat "$WORK/data/harbor.db")" "OLD-DB" "the database was not replaced"
	teardown
}

case_snapshot_failure_aborts_before_pinning() {
	current="snapshot failure: refuses to update, env untouched"
	setup
	touch "$STATE/snapshot_fails"
	run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_eq "$(cat "$WORK/.env")" "$(cat "$WORK/.env.original")" "env is byte-identical"
	assert_not_contains "$(calls)" "pull"
	assert_contains "$(status_json)" "snapshot failed"
	teardown
}

case_unhealthy_rolls_back_and_restores() {
	current="unhealthy: rolls back, restores the snapshot, re-pins the old digest"
	# SECONDS is the suite's clock, not this case's. Measured from the suite start
	# it fails whenever the earlier cases took ten seconds between them, which has
	# nothing to do with the health window.
	local started=$SECONDS
	setup
	rm -f "$STATE/healthy"
	HEALTH_TIMEOUT=1 run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_eq "$(image_line)" "HARBOR_IMAGE=$REPO@$OLD_DIGEST" "old digest restored"
	assert_eq "$(cat "$WORK/data/harbor.db")" "SNAPSHOT-CONTENT" "the snapshot was restored"
	assert_absent "$WORK/data/harbor.db-wal"
	assert_contains "$(calls)" "stop harbor"
	assert_contains "$(status_json)" "rolled back"
	assert_contains "$(status_json)" '"status":"failed"'

	# The health window is bounded: 1 s plus one interval, not the 120 s default.
	local elapsed=$((SECONDS - started))
	if [ "$elapsed" -lt 10 ]; then
		ok "the health window is bounded"
	else
		no "the health window is bounded" "took ${elapsed}s for a 1s window"
	fi
	teardown
}

case_unhealthy_without_snapshot_still_rolls_back() {
	current="unhealthy with no snapshot: still re-pins, still reports failure"
	setup
	rm -f "$STATE/healthy" "$WORK/data/backups/pre-update.db"
	HEALTH_TIMEOUT=1 run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_eq "$(image_line)" "HARBOR_IMAGE=$REPO@$OLD_DIGEST" "old digest restored"
	assert_contains "$(status_json)" "still unhealthy"
	teardown
}

case_resolve_failure_changes_nothing() {
	current="registry unreachable: reports, changes nothing"
	setup
	touch "$STATE/resolve_fails"
	run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_eq "$(cat "$WORK/.env")" "$(cat "$WORK/.env.original")" "env is byte-identical"
	assert_not_contains "$(calls)" "snapshot"
	assert_contains "$(status_json)" "could not resolve"
	teardown
}

case_a_bad_digest_is_refused() {
	current="a malformed digest from the registry is refused"
	setup
	touch "$STATE/bad_digest"
	run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_eq "$(cat "$WORK/.env")" "$(cat "$WORK/.env.original")" "env is byte-identical"
	assert_contains "$(status_json)" "could not resolve"
	teardown
}

case_requested_must_be_latest() {
	current="requested with an image name: refused, nothing changes"
	setup
	printf 'ghcr.io/evil/harbor:latest\n' >"$WORK/update-signal/requested"
	run_watcher

	assert_eq "$WATCHER_EXIT" "1" "exit code"
	assert_eq "$(cat "$WORK/.env")" "$(cat "$WORK/.env.original")" "env is byte-identical"
	assert_contains "$(status_json)" "refused requested image"
	assert_absent "$WORK/update-signal/requested"
	assert_not_contains "$(calls)" "pull"
	teardown
}

case_requested_latest_updates() {
	current="requested with the literal latest: proceeds, consumes the file"
	setup
	printf 'latest\n' >"$WORK/update-signal/requested"
	run_watcher

	assert_eq "$WATCHER_EXIT" "0" "exit code"
	assert_absent "$WORK/update-signal/requested"
	assert_eq "$(image_line)" "HARBOR_IMAGE=$REPO@$NEW_DIGEST" "digest pinned"
	teardown
}

case_lock_contention() {
	current="a live lock: reports busy and touches nothing"
	setup
	mkdir -p "$WORK/update-signal/.lock.d"
	# Our own shell is alive, so the lock is held by a running process.
	printf '%s\n' "$$" >"$WORK/update-signal/.lock.d/pid"
	run_watcher

	assert_eq "$WATCHER_EXIT" "0" "exit code"
	assert_contains "$(status_json)" '"status":"busy"'
	assert_eq "$(cat "$WORK/.env")" "$(cat "$WORK/.env.original")" "env is byte-identical"
	assert_eq "$(calls)" "" "no docker calls at all"
	teardown
}

case_stale_lock_is_taken_over() {
	current="a stale lock (dead pid): taken over instead of blocking forever"
	setup
	mkdir -p "$WORK/update-signal/.lock.d"
	# A pid that cannot be running: start high and hope; verify with kill -0 first.
	printf '999999\n' >"$WORK/update-signal/.lock.d/pid"
	run_watcher

	assert_eq "$WATCHER_EXIT" "0" "exit code"
	assert_eq "$(image_line)" "HARBOR_IMAGE=$REPO@$NEW_DIGEST" "the update ran"
	assert_absent "$WORK/update-signal/.lock.d"
	teardown
}

case_update_dir_is_created() {
	current="a fresh host with no update-signal directory still works"
	setup
	rm -rf "$WORK/update-signal"
	run_watcher

	assert_eq "$WATCHER_EXIT" "0" "exit code"
	assert_file "$WORK/update-signal/status.json"
	assert_file "$WORK/update-signal/heartbeat"
	teardown
}

case_tick_writes_heartbeat_and_catches_up() {
	current="tick: heartbeat every run, starts the update service only when a signal waits"
	setup
	FAKE_STATE="$STATE" PATH="$WORK/bin:$PATH" HARBOR_ROOT="$WORK" HARBOR_SIGNAL_DIR="$WORK/update-signal" \
		bash "$TICK" >/dev/null 2>&1
	assert_file "$WORK/update-signal/heartbeat"
	assert_eq "$(systemctl_calls)" "" "no signal, no service start"

	printf 'latest\n' >"$WORK/update-signal/requested"
	FAKE_STATE="$STATE" PATH="$WORK/bin:$PATH" HARBOR_ROOT="$WORK" HARBOR_SIGNAL_DIR="$WORK/update-signal" \
		bash "$TICK" >/dev/null 2>&1
	assert_contains "$(systemctl_calls)" "start harbor-update.service" "the tick catches up a waiting signal"
	teardown
}

# ── run ─────────────────────────────────────────────────────────────────────

for case in \
	case_bare_start_updates_and_pins \
	case_check_only_changes_nothing \
	case_check_reports_up_to_date \
	case_second_update_is_a_no_op \
	case_reconcile_does_not_report_ok_when_unhealthy \
	case_failed_check_does_not_advertise_an_update \
	case_pull_failure_re_pins_the_old_digest \
	case_snapshot_failure_aborts_before_pinning \
	case_unhealthy_rolls_back_and_restores \
	case_unhealthy_without_snapshot_still_rolls_back \
	case_resolve_failure_changes_nothing \
	case_a_bad_digest_is_refused \
	case_requested_must_be_latest \
	case_requested_latest_updates \
	case_lock_contention \
	case_stale_lock_is_taken_over \
	case_update_dir_is_created \
	case_tick_writes_heartbeat_and_catches_up; do
	"$case"
done

echo
if [ "$fail" -ne 0 ]; then
	echo "watcher: $fail of $((pass + fail)) checks failed" >&2
	exit 1
fi
echo "watcher: $pass checks passed"
