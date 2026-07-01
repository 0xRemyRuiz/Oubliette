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
if [[ "${1:-}" == "--yes" ]]; then
  ASSUME_YES=1
fi

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
state="$(virsh domstate "${FT_DOMAIN}" 2>/dev/null || echo "missing")"
if [[ "${state}" == "running" ]]; then
  echo "Destroying running domain..."
  virsh destroy "${FT_DOMAIN}" >/dev/null 2>&1 || true
fi

# --- Undefine the domain ---
# --nvram covers UEFI/nvram if it was ever added; harmless otherwise.
# We do NOT pass --remove-all-storage because we manage artifact
# deletion explicitly below (and want to spare the base image).
if virsh dominfo "${FT_DOMAIN}" >/dev/null 2>&1; then
  echo "Undefining domain '${FT_DOMAIN}'..."
  virsh undefine "${FT_DOMAIN}" --nvram >/dev/null 2>&1 \
    || virsh undefine "${FT_DOMAIN}" >/dev/null 2>&1 \
    || true
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