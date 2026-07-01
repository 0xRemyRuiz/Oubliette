#!/usr/bin/env bash
# Destroy the L1 devhost VM and its overlay disk + seed.
# Keeps the base cloud image and the SSH key so re-create is cheap.
#   ./destroy.sh          # prompts
#   ./destroy.sh --yes    # no prompt

set -euo pipefail
source "$(dirname "$0")/devhost-env.sh"

ASSUME_YES=0
[[ "${1:-}" == "--yes" ]] && ASSUME_YES=1

# --- Confirmation ---
if [[ "${ASSUME_YES}" -eq 0 ]]; then
  echo "This will undefine '${DH_DOMAIN}' and delete:"
  echo "  - overlay disk: ${DH_DISK}"
  echo "  - seed dir:     ${DH_SEED_DIR}"
  echo "(Base image ${DH_CLOUD_IMG} and ssh key ${DH_SSH_KEY} are kept.)"
  read -r -p "Proceed? [y/N] " reply
  case "${reply}" in [yY]|[yY][eE][sS]) ;; *) echo "Aborted."; exit 0 ;; esac
fi

# --- Stop the domain if running ---
state="$(virsh -c "${DH_CONNECT}" domstate "${DH_DOMAIN}" 2>/dev/null || echo missing)"
if [[ "${state}" == "running" ]]; then
  echo "Destroying running domain..."
  virsh -c "${DH_CONNECT}" destroy "${DH_DOMAIN}" >/dev/null 2>&1 || true
fi

# --- Undefine the domain ---
if virsh -c "${DH_CONNECT}" dominfo "${DH_DOMAIN}" >/dev/null 2>&1; then
  echo "Undefining domain..."
  virsh -c "${DH_CONNECT}" undefine "${DH_DOMAIN}" --nvram >/dev/null 2>&1 \
    || virsh -c "${DH_CONNECT}" undefine "${DH_DOMAIN}" >/dev/null 2>&1 || true
else
  echo "Domain not defined; continuing with artifact cleanup."
fi

[[ -f "${DH_DISK}" ]]    && rm -f "${DH_DISK}"    && echo "Removed overlay disk."
[[ -d "${DH_SEED_DIR}" ]] && rm -rf "${DH_SEED_DIR}" && echo "Removed seed dir."

echo "Done. '${DH_DOMAIN}' removed. Base image and ssh key retained."