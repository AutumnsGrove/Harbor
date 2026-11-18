#!/usr/bin/env bash
# Harbor's egress model, in one script.
#
# The container must reach the public internet (pages, PDFs, images, OpenRouter,
# the Worker) and must not reach the homelab, the tailnet, loopback or the cloud
# metadata range. A hostname allowlist is impossible here — Harbor fetches
# arbitrary pages by design — so the rule is address-based and lives in
# DOCKER-USER, which Docker does not otherwise touch.
#
#   install:   sudo deploy/docker-user-firewall.sh
#   check:     sudo deploy/docker-user-firewall.sh --check
#   remove:    sudo deploy/docker-user-firewall.sh --remove
#
# The compose file pins the network to 172.31.240.0/24 so these rules can name
# it. Docker's rules do not survive a reboot; deploy/harbor-firewall.service
# re-runs this script at boot.
set -euo pipefail

SUBNET="${HARBOR_EGRESS_SUBNET:-172.31.240.0/24}"

# The compose network is IPv4-only (no `enable_ipv6`), so a container on it has no
# IPv6 address and there is no v6 source to scope a rule to. Applying the v4
# subnet to ip6tables is an error, not a fallback. Set this only if the network
# ever gains IPv6.
SUBNET6="${HARBOR_EGRESS_SUBNET6:-}"

# Everything inside the homelab, the tailnet, loopback and link-local. 100.64/10
# is the Tailscale CGNAT range; fd7a:115c:a1e0::/48 is the tailnet ULA.
V4_DENY=(
	10.0.0.0/8
	172.16.0.0/12
	192.168.0.0/16
	127.0.0.0/8
	169.254.0.0/16
	100.64.0.0/10
)
V6_DENY=(
	fd7a:115c:a1e0::/48
	fc00::/7
	fe80::/10
	::1/128
)

# Values edited here so the unit and a hand run behave the same.
IPTABLES="${IPTABLES:-iptables}"
IP6TABLES="${IP6TABLES:-ip6tables}"

require_root() {
	if [ "$(id -u)" -ne 0 ]; then
		echo "harbor-firewall: run as root (sudo $0 $*)" >&2
		exit 1
	fi
}

have_ip6() {
	command -v "$IP6TABLES" >/dev/null 2>&1
}

# rule_exists <tool> <spec...> — true when the exact rule is already installed.
rule_exists() {
	local tool=$1
	shift
	"$tool" -C "$@" >/dev/null 2>&1
}

add_rule() {
	local tool=$1
	shift
	if ! rule_exists "$tool" "$@"; then
		"$tool" -I "$@"
	fi
}

del_rule() {
	local tool=$1
	shift
	while rule_exists "$tool" "$@"; do
		"$tool" -D "$@"
	done
}

apply() {
	# DOCKER-USER exists as soon as the docker service has run once.
	if ! "$IPTABLES" -n -L DOCKER-USER >/dev/null 2>&1; then
		echo "harbor-firewall: no DOCKER-USER chain; is docker running?" >&2
		exit 1
	fi

	for target in "${V4_DENY[@]}"; do
		add_rule "$IPTABLES" DOCKER-USER 1 -s "$SUBNET" -d "$target" -j REJECT
	done

	if [ -n "$SUBNET6" ] && have_ip6; then
		for target in "${V6_DENY[@]}"; do
			add_rule "$IP6TABLES" DOCKER-USER 1 -s "$SUBNET6" -d "$target" -j REJECT
		done
	fi

	echo "harbor-firewall: $SUBNET may reach the internet, not the homelab or the tailnet"
	if [ -z "$SUBNET6" ]; then
		echo "harbor-firewall: the compose network is IPv4-only; no v6 rules applied"
	fi
}

remove() {
	for target in "${V4_DENY[@]}"; do
		del_rule "$IPTABLES" DOCKER-USER -s "$SUBNET" -d "$target" -j REJECT
	done
	if [ -n "$SUBNET6" ] && have_ip6; then
		for target in "${V6_DENY[@]}"; do
			del_rule "$IP6TABLES" DOCKER-USER -s "$SUBNET6" -d "$target" -j REJECT
		done
	fi
	echo "harbor-firewall: rules removed"
}

check() {
	local missing=0
	for target in "${V4_DENY[@]}"; do
		if ! rule_exists "$IPTABLES" DOCKER-USER -s "$SUBNET" -d "$target" -j REJECT; then
			echo "missing: $SUBNET -> $target" >&2
			missing=1
		fi
	done
	if [ -n "$SUBNET6" ] && have_ip6; then
		for target in "${V6_DENY[@]}"; do
			if ! rule_exists "$IP6TABLES" DOCKER-USER -s "$SUBNET6" -d "$target" -j REJECT; then
				echo "missing: $SUBNET -> $target (v6)" >&2
				missing=1
			fi
		done
	fi
	if [ "$missing" -ne 0 ]; then
		echo "harbor-firewall: rules are incomplete" >&2
		exit 1
	fi
	echo "harbor-firewall: all rules present"
}

case "${1:---apply}" in
--apply | apply)
	require_root "$@"
	apply
	;;
--remove | remove)
	require_root "$@"
	remove
	;;
--check | check)
	require_root "$@"
	check
	;;
*)
	echo "usage: $0 [--apply|--check|--remove]" >&2
	exit 2
	;;
esac
