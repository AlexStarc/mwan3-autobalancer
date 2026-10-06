#!/bin/sh
# Shared shell-only lease validation. Never source runtime JSON as shell code.
. /usr/share/libubox/jshn.sh
AB_RUN=/var/run/mwan3-autobalancer
AB_LIB=/usr/libexec/mwan3-autobalancer

ab_id() { case "$1" in ''|[!A-Za-z0-9_]*|*[!A-Za-z0-9_-]*) return 1;; esac; [ "${#1}" -le 32 ]; }
ab_policy() { ab_id "$1" && [ "${#1}" -le 15 ]; }
ab_pid() { case "$1" in ''|*[!0-9]*|0*) return 1;; esac; [ "${#1}" -le 10 ] && [ "$1" -gt 1 ]; }
ab_session() { case "$1" in *[!0-9a-f]*) return 1;; esac; [ "${#1}" -eq 32 ]; }
ab_number() { printf '%s\n' "$1" | awk '/^[0-9]+([.][0-9]+)?$/ { ok=1 } END { exit !ok }'; }
ab_uptime() { read -r ab_up ab_idle < /proc/uptime; ab_number "$ab_up" || return 1; printf '%s\n' "$ab_up"; }
ab_json_file() {
	[ -f "$1" ] && [ ! -L "$1" ] && [ "$(wc -c < "$1")" -le 16384 ] || return 1
	json_load "$(cat "$1")"
}
ab_lease() {
	ab_json_file "$AB_RUN/lease.json" || return 1
	json_get_var lease_policy policy; json_get_var lease_pid pid
	json_get_var lease_session session; json_get_var lease_heartbeat heartbeat_file
	json_get_var lease_uptime uptime; json_get_var lease_force restore_requested
	local kind
	json_get_type kind policy; [ "$kind" = string ] || return 1
	json_get_type kind session; [ "$kind" = string ] || return 1
	json_get_type kind heartbeat_file; [ "$kind" = string ] || return 1
	json_get_type kind pid; [ "$kind" = int ] || return 1
	json_get_type kind uptime; [ "$kind" = int ] || [ "$kind" = double ] || return 1
	json_get_type kind restore_requested
	[ -z "$kind" ] || [ "$kind" = boolean ] || return 1
	ab_policy "$lease_policy" && ab_pid "$lease_pid" && ab_session "$lease_session" && ab_number "$lease_uptime" || return 1
	[ "$lease_heartbeat" = "heartbeat.$lease_pid.json" ]
}
ab_owner_alive() {
	ab_pid "$1" && kill -0 "$1" 2>/dev/null && [ -r "/proc/$1/cmdline" ] || return 1
	# Require exact command arguments; a status subprocess is never a lease owner.
	tr '\000' '\n' < "/proc/$1/cmdline" | awk '
		$0 == "/usr/bin/mwan3-autobalancer" { binary=1 }
		$0 == "daemon" || $0 == "once" { owner=1 }
		END { exit !(binary && owner) }'
}
ab_expired() {
	[ "$lease_force" = 1 ] && return 0
	ab_owner_alive "$lease_pid" || return 0
	ab_json_file "$AB_RUN/$lease_heartbeat" || return 0
	local pid session uptime now kind
	json_get_var pid pid; json_get_var session session; json_get_var uptime uptime
	json_get_type kind pid; [ "$kind" = int ] || return 0
	json_get_type kind session; [ "$kind" = string ] || return 0
	json_get_type kind uptime; [ "$kind" = int ] || [ "$kind" = double ] || return 0
	[ "$pid" = "$lease_pid" ] && [ "$session" = "$lease_session" ] && ab_number "$uptime" || return 0
	now=$(ab_uptime) || return 1
	awk -v now="$now" -v beat="$uptime" 'BEGIN { exit !(beat > now || now-beat > 90) }'
}
ab_selected_policy() {
	local selected
	selected=$(uci -q get mwan3_autobalancer.main.policy) || selected=balanced
	[ -n "$selected" ] || selected=balanced
	ab_policy "$selected" || return 1
	printf '%s\n' "$selected"
}
