#!/usr/bin/env bash
# Standalone bridge: turn Suricata EVE alerts into oubliette containment
# requests. It tails Suricata's eve.json, and for every alert whose flow is a
# session into the broker port, it sends a `contain` request to the broker's
# Unix control socket -- dropping that exact session into the shadow VM.
#
# Suricata detects; oubliette contains. This process is the glue between them.
#
# Run it as root (eve.json and the 0600 control socket are root-owned), on the
# host where the broker (`oubliette serve --control ...`) runs.
#
# Configuration (env overrides):
#   EVE_LOG      path to Suricata's eve.json   (default /var/log/suricata/eve.json)
#   BROKER_PORT  TCP port the broker listens on (default 2222)
#   CONTROL_SOCK broker control socket path     (default /run/oubliette.sock)
#
# Usage:
#   sudo ./vm/debian/suricata-contain-bridge.sh
#   sudo BROKER_PORT=2222 CONTROL_SOCK=/run/oubliette.sock ./vm/debian/suricata-contain-bridge.sh

set -euo pipefail

EVE_LOG="${EVE_LOG:-/var/log/suricata/eve.json}"
BROKER_PORT="${BROKER_PORT:-2222}"
CONTROL_SOCK="${CONTROL_SOCK:-/run/oubliette.sock}"

log() { printf '%s %s\n' "$(date -Is)" "$*"; }

# --- Require root (read root-owned eve.json, connect to 0600 socket) ---
if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root: sudo $0" >&2
  exit 1
fi

# --- Require the tools ---
for tool in jq socat tail; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "Missing required tool: ${tool}" >&2
    exit 1
  fi
done

# --- Advisory checks (not fatal: both may appear after we start) ---
if [[ ! -e "${EVE_LOG}" ]]; then
  log "note: ${EVE_LOG} does not exist yet; waiting for Suricata to create it."
fi
if [[ ! -S "${CONTROL_SOCK}" ]]; then
  log "note: control socket ${CONTROL_SOCK} not present yet; start the broker with --control ${CONTROL_SOCK}."
fi

log "bridging Suricata alerts (dest_port=${BROKER_PORT}) from ${EVE_LOG} to ${CONTROL_SOCK}"

# Dedup: Suricata often re-fires on the same flow. springTrap is idempotent, but
# we skip repeats to keep the log clean. State persists because the while loop
# runs in this shell (process substitution), not a pipe subshell.
declare -A seen

# jq extracts "src_ip<TAB>src_port" for each matching alert. Customize the
# select() to narrow by signature: add `and .alert.signature_id==9000001`, or
# `and .alert.severity<=2` for high-severity only.
while IFS=$'\t' read -r ip port; do
  [[ -z "${ip}" || -z "${port}" ]] && continue
  key="${ip}:${port}"
  if [[ -n "${seen[${key}]:-}" ]]; then
    continue
  fi
  seen[${key}]=1

  req="{\"action\":\"contain\",\"match\":{\"remote_ip\":\"${ip}\",\"remote_port\":${port}}}"
  if resp="$(printf '%s\n' "${req}" | socat - "UNIX-CONNECT:${CONTROL_SOCK}" 2>&1)"; then
    log "contain ${key} -> ${resp}"
  else
    log "contain ${key} FAILED: ${resp}"
  fi
done < <(
  tail -F "${EVE_LOG}" \
    | jq -rc --unbuffered --argjson port "${BROKER_PORT}" \
        'select(.event_type=="alert" and .dest_port==$port) | "\(.src_ip)\t\(.src_port)"'
)
