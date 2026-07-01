#!/usr/bin/env bash
# Destroy the falltrap research VM and remove its per-domain artifacts.
# Leaves the downloaded base cloud image (${FT_CLOUD_IMG}) intact so a
# subsequent create doesn't need to re-download it.
#
# Usage:
#   ./destroy.sh           # prompts for confirmation
#   ./destroy.sh --yes     # skip confirmation

set -euo pipefail
source "$(dirname "$0")/falltrap-env.sh"

ASSUME_YES=0
[[ "${1:-}" == "--yes" ]] && ASSUME_YES=1

# --- Confirmation ---
if [[ "${ASSUME_YES}" -eq 0 ]]; then
  echo "This will undefine domain '${FT_DOMAIN}' and delete:"
  echo "  - overlay disk:  ${FT_DISK}"
  echo "  - seed dir:      ${FT_SEED_DIR}"
  echo "  - shared dir:    ${FT_SHARED_DIR}"
  echo "(The base cloud image ${FT_CLOUD_IMG} will be kept.)"
  read -r -p "Proceed? [y/N] " reply
  case "${reply}" in
    [yY]|[yY][eE][sS]) ;;
    *) echo "Aborted."; exit 0 ;;
  esac
fi

# --- Stop the domain if running ---
state="$(virsh -c "${FT_CONNECT}" domstate "${FT_DOMAIN}" 2>/dev/null || echo missing)"
if [[ "${state}" == "running" ]]; then
  echo "Destroying running domain..."
  virsh -c "${FT_CONNECT}" destroy "${FT_DOMAIN}" >/dev/null 2>&1 || true
fi

# --- Undefine the domain ---
if virsh -c "${FT_CONNECT}" dominfo "${FT_DOMAIN}" >/dev/null 2>&1; then
  echo "Undefining domain..."
  virsh -c "${FT_CONNECT}" undefine "${FT_DOMAIN}" --nvram >/dev/null 2>&1 \
    || virsh -c "${FT_CONNECT}" undefine "${FT_DOMAIN}" >/dev/null 2>&1 || true
else
  echo "Domain '${FT_DOMAIN}' not defined; continuing with artifact cleanup."
fi

# --- Remove per-domain artifacts ---
# Guard each removal so a partial state doesn't abort the whole cleanup.
if [[ -f "${FT_DISK}" ]]; then
  rm -f "${FT_DISK}"
  echo "Removed overlay disk."
fi

if [[ -d "${FT_SEED_DIR}" ]]; then
  rm -rf "${FT_SEED_DIR}"
  echo "Removed seed dir."
fi

if [[ -d "${FT_SHARED_DIR}" ]]; then
  rm -rf "${FT_SHARED_DIR}"
  echo "Removed shared dir."
fi

echo "Done. Domain '${FT_DOMAIN}' and its artifacts are gone."
echo "Base image retained at: ${FT_CLOUD_IMG}"