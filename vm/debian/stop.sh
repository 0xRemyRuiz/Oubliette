#!/usr/bin/env bash
# Stop the falltrap VM.
# Default: graceful shutdown. Pass --force for immediate destroy.

set -euo pipefail
source "$(dirname "$0")/falltrap-env.sh"

FORCE=0
if [[ "${1:-}" == "--force" ]]; then
  FORCE=1
fi

state="$(virsh -c "${FT_CONNECT}" domstate "${FT_DOMAIN}" 2>/dev/null || echo "missing")"

case "${state}" in
  running)
    if [[ "${FORCE}" -eq 1 ]]; then
      virsh -c "${FT_CONNECT}" destroy "${FT_DOMAIN}" >/dev/null
      echo "Domain '${FT_DOMAIN}' destroyed (forced)."
    else
      virsh -c "${FT_CONNECT}" shutdown "${FT_DOMAIN}" >/dev/null
      echo -n "Shutting down '${FT_DOMAIN}'"
      for _ in $(seq 1 30); do
        s="$(virsh -c "${FT_CONNECT}" domstate "${FT_DOMAIN}")"
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
    echo "Domain '${FT_DOMAIN}' already stopped."
    ;;
  missing)
    echo "Domain '${FT_DOMAIN}' does not exist." >&2
    exit 1
    ;;
  *)
    echo "Domain '${FT_DOMAIN}' in unexpected state: ${state}" >&2
    exit 1
    ;;
esac