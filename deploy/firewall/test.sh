#!/usr/bin/env bash
# Tests for the egress firewall script, with a fake iptables.
#
# The bug this exists for: `add_rule` passed the insert position into the
# existence check (`iptables -C CHAIN 1 -s …`), the check never matched, and every
# restart duplicated the whole rule set. On the potato that was twelve rules where
# six belong, and only a live deploy found it.
#
#   ./deploy/firewall/test.sh
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/../docker-user-firewall.sh"

pass=0
fail=0
current=""

ok() {
	pass=$((pass + 1))
	printf '  ok   %s — %s\n' "$current" "${1:-}"
}
no() {
	fail=$((fail + 1))
	printf '  FAIL %s — %s\n' "$current" "${1:-}"
	[ -n "${2:-}" ] && printf '       %s\n' "$2"
	return 0
}

assert_eq() {
	local message="${3:-equality}"
	if [ "$1" = "$2" ]; then ok "$message"; else no "$message" "expected [$2], got [$1]"; fi
}

assert_contains() {
	local message="${3:-contains}"
	if printf '%s' "$1" | grep -qF -- "$2"; then ok "$message"; else no "$message" "expected [$1] to contain [$2]"; fi
}

assert_not_contains() {
	local message="${3:-does not contain}"
	if printf '%s' "$1" | grep -qF -- "$2"; then no "$message" "expected [$1] not to contain [$2]"; else ok "$message"; fi
}

setup() {
	WORK="$(mktemp -d)"
	STATE="$WORK/state"
	mkdir -p "$WORK/bin" "$STATE"
	: >"$STATE/rules"
	: >"$STATE/calls"

	# A fake iptables that keeps just enough state: `-I` records a rule, `-C`
	# looks for exactly that rule, `-D` removes one copy, `-S` prints them, and
	# `-n -L` claims the chain exists.
	for tool in iptables ip6tables; do
		cat >"$WORK/bin/$tool" <<'FAKE'
#!/usr/bin/env bash
set -uo pipefail
printf '%s %s\n' "$(basename "$0")" "$*" >>"$FAKE_STATE/calls"
args=("$@")
case "${args[0]:-}" in
-C)
	shift
	chain="${1:-}"
	shift
	rule="$*"
	grep -qxF -- "$chain|$rule" "$FAKE_STATE/rules"
	;;
-I)
	shift
	chain="${1:-}"
	shift
	# Drop the position argument: it is not part of the rule.
	shift
	rule="$*"
	printf '%s|%s\n' "$chain" "$rule" >>"$FAKE_STATE/rules"
	;;
-D)
	shift
	chain="${1:-}"
	shift
	rule="$*"
	if grep -qxF -- "$chain|$rule" "$FAKE_STATE/rules"; then
		grep -vxF -- "$chain|$rule" "$FAKE_STATE/rules" >"$FAKE_STATE/rules.tmp"
		mv "$FAKE_STATE/rules.tmp" "$FAKE_STATE/rules"
	else
		exit 1
	fi
	;;
-S)
	cut -d'|' -f2 "$FAKE_STATE/rules"
	;;
-n)
	exit 0
	;;
*)
	exit 0
	;;
esac
FAKE
		chmod +x "$WORK/bin/$tool"
	done

	# `id -u` must look like root to the script.
	cat >"$WORK/bin/id" <<'FAKE'
#!/usr/bin/env bash
[ "${1:-}" = "-u" ] && { echo 0; exit 0; }
exec /usr/bin/id "$@"
FAKE
	chmod +x "$WORK/bin/id"
}

run_firewall() {
	: >"$STATE/calls"
	FAKE_STATE="$STATE" PATH="$WORK/bin:$PATH" bash "$SCRIPT" "$@" >"$WORK/out.log" 2>&1
	EXIT=$?
	return 0
}

rules() { cut -d'|' -f2 "$STATE/rules" 2>/dev/null | sort; }
rule_count() { wc -l <"$STATE/rules" | tr -d ' '; }

case_apply_twice_is_idempotent() {
	current="apply twice: six rules, not twelve"
	setup
	run_firewall --apply
	first="$(rule_count)"
	run_firewall --apply
	second="$(rule_count)"

	assert_eq "$EXIT" "0" "exit code"
	assert_eq "$first" "6" "the first run installs six rules"
	assert_eq "$second" "6" "the second run adds nothing"
	assert_eq "$(grep -c '^-I' "$STATE/calls" || true)" "0" "the second run inserts no rule"

	# The position never reaches the existence check.
	assert_not_contains "$(cat "$STATE/calls")" "-C DOCKER-USER 1" "the check has no position argument"
	assert_contains "$(cat "$STATE/calls")" "-C DOCKER-USER -s 172.31.240.0/24 -d 10.0.0.0/8 -j REJECT" "the check is the rule itself"
	teardown
}

case_remove_clears_everything() {
	current="remove: every rule goes, and a second remove is harmless"
	setup
	run_firewall --apply
	run_firewall --remove
	assert_eq "$(rule_count)" "0" "no rules left"
	run_firewall --remove
	assert_eq "$EXIT" "0" "a second remove succeeds"
	teardown
}

case_check_reports_missing() {
	current="check: fails before apply, passes after"
	setup
	run_firewall --check
	assert_eq "$EXIT" "1" "check before apply fails"
	run_firewall --apply
	run_firewall --check
	assert_eq "$EXIT" "0" "check after apply passes"
	teardown
}

case_v6_is_skipped_by_default() {
	current="v6: no source subnet, no ip6tables calls"
	setup
	run_firewall --apply
	assert_not_contains "$(cat "$STATE/calls")" "ip6tables" "ip6tables is never invoked"
	assert_contains "$(cat "$WORK/out.log")" "IPv4-only" "the script says why"
	teardown
}

case_v6_rules_when_configured() {
	current="v6: an explicit subnet installs the four v6 rules, once"
	setup
	run_v6() {
		: >"$STATE/calls"
		FAKE_STATE="$STATE" PATH="$WORK/bin:$PATH" HARBOR_EGRESS_SUBNET6=fd00:dead::/64 \
			bash "$SCRIPT" --apply >/dev/null 2>&1
	}

	run_v6
	assert_eq "$(grep -c '^ip6tables -I' "$STATE/calls" || true)" "4" "four v6 inserts"

	# The v6 rules are as idempotent as the v4 ones: a second apply inserts nothing.
	run_v6
	assert_eq "$(grep -c '^ip6tables -I' "$STATE/calls" || true)" "0" "no v6 insert on the second run"
	assert_eq "$(grep -c 'fd7a:115c:a1e0::/48' "$STATE/rules" || true)" "1" "the tailnet ULA is denied once"
	teardown
}

teardown() { rm -rf "$WORK"; }

for case in \
	case_apply_twice_is_idempotent \
	case_remove_clears_everything \
	case_check_reports_missing \
	case_v6_is_skipped_by_default \
	case_v6_rules_when_configured; do
	"$case"
done

echo
if [ "$fail" -ne 0 ]; then
	echo "firewall: $fail of $((pass + fail)) checks failed" >&2
	exit 1
fi
echo "firewall: $pass checks passed"
