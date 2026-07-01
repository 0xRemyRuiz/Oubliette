#!/usr/bin/env bash
# Stop the L1 devhost VM.
# Default: graceful shutdown. Pass --force for immediate destroy.

set -euo pipefail
source "$(dirname "$0")/devhost-env.sh"

FORCE=0
if [[ "${1:-}" == "--force" ]]; then
  FORCE=1
fi

state="$(virsh -c "${DH_CONNECT}" domstate "${DH_DOMAIN}" 2>/dev/null || echo "missing")"

case "${state}" in
  running)
    if [[ "${FORCE}" -eq 1 ]]; then
      virsh -c "${DH_CONNECT}" destroy "${DH_DOMAIN}" >/dev/null
      echo "Domain '${DH_DOMAIN}' destroyed (forced)."
    else
      virsh -c "${DH_CONNECT}" shutdown "${DH_DOMAIN}" >/dev/null
      echo -n "Shutting down '${DH_DOMAIN}'"
      for _ in $(seq 1 30); do
        s="$(virsh -c "${DH_CONNECT}" domstate "${DH_DOMAIN}")"
        [[ "${s}" == "shut off" ]] && { echo " done."; exit 0; }
        echo -n "."
        sleep 1
      done
      echo
      echo "Graceful shutdown timed out; consider --force." >&2
      exit 1
    fi
    ;;
  "shut off")
    echo "Domain '${DH_DOMAIN}' already stopped."
    ;;
  missing)
    echo "Domain '${DH_DOMAIN}' does not exist." >&2
    exit 1
    ;;
  *)
    echo "Domain '${DH_DOMAIN}' in unexpected state: ${state}" >&2
    exit 1
    ;;
esac
