#!/usr/bin/env bash
#
# Removes host state left behind by an interrupted or crashed run: firecracker
# processes, tap devices, the bridge, and NAT/forwarding rules.
#
# The launcher cleans up after itself on exit, but a SIGKILL or a panic leaves
# the bridge and rules in place. Run this to get back to a clean host.
#
# Earlier revisions of this project used br0 on 192.168.1.0/24 (which collides
# with a typical home LAN) and appended a duplicate MASQUERADE rule on every
# run, so this also cleans up after those.

set -uo pipefail

BRIDGE="${BRIDGE:-br-fc}"
SUBNET="${SUBNET:-10.200.0.0/24}"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }

UPLINK="$(ip route get 8.8.8.8 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev") print $(i+1); exit}')"

if pgrep -x firecracker >/dev/null 2>&1; then
  log "stopping firecracker processes"
  sudo pkill -x firecracker || true
  sleep 1
fi

# drop_rule <table> <spec...> — deletes every copy of a rule, not just the first.
drop_rule() {
  local table="$1"; shift
  while sudo iptables -t "${table}" -C "$@" 2>/dev/null; do
    log "removing ${table} rule: $*"
    sudo iptables -t "${table}" -D "$@" || break
  done
}

# clean_bridge <bridge> <subnet>
clean_bridge() {
  local bridge="$1" subnet="$2"

  for tap in "${bridge}-tap-0" "${bridge}-tap-1"; do
    if ip link show "${tap}" >/dev/null 2>&1; then
      log "removing tap ${tap}"
      sudo ip link delete "${tap}" || true
    fi
  done

  if [[ -n "${UPLINK}" ]]; then
    drop_rule nat POSTROUTING -s "${subnet}" -o "${UPLINK}" -j MASQUERADE
    drop_rule nat POSTROUTING -o "${UPLINK}" -j MASQUERADE
    drop_rule filter FORWARD -i "${bridge}" -o "${UPLINK}" -j ACCEPT
    drop_rule filter FORWARD -i "${UPLINK}" -o "${bridge}" \
      -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
  fi

  # Bridge-scoped MASQUERADE, in case an earlier run installed one.
  drop_rule nat POSTROUTING -o "${bridge}" -j MASQUERADE

  if ip link show "${bridge}" >/dev/null 2>&1; then
    log "removing bridge ${bridge}"
    sudo ip link delete "${bridge}" || true
  fi
}

clean_bridge "${BRIDGE}" "${SUBNET}"
clean_bridge "br0" "192.168.1.0/24"

sudo rm -f /tmp/firecracker*.sock /tmp/firecracker*.sock.log /tmp/firecracker*.sock-metrics

log "host is clean"
