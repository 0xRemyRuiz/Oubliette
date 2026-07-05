#!/usr/bin/env bash
# Make the falltrap guest a coherent copy of *this* host's interactive-shell
# environment, so a CRIU restore finds every open/mmap'd file at the same path
# with matching content and mode.
#
# Why this is needed: oubliette restores the migrated process into the guest's
# EXISTING namespaces (criu "NS mask 0"), not the dumped mount namespace. That
# means criu does not recreate the process's files -- it opens them from the
# guest's real filesystem and rejects any that are absent or whose mode does
# not match the dump. For an interactive fish session the relevant state is:
#
#   1. the fish binary and its shared libraries  (must be byte-identical --
#      file-backed VMAs won't restore otherwise); satisfied by installing the
#      same Debian package version, which this script verifies by checksum.
#   2. the user's home directory                 (fish_history, fish_variables,
#      config, and anything else the shell has open), mirrored here.
#
# It deliberately does NOT copy transient /run/user/<uid> FIFOs/sockets: those
# are per-session runtime artifacts recreated on the restore side.
#
# Run this on the source host (where the fish session to migrate lives), with
# the falltrap guest already started (start.sh). Idempotent; re-run any time
# the source session's on-disk state changes before migrating.

set -euo pipefail

# Must run as the migrating user, not root: this script derives the SSH key
# path and the home directory to mirror from $HOME. Under sudo, $HOME is /root,
# so the key path and the copied home would both be wrong. virsh access here
# comes from libvirt-group membership (as in shell.sh/start.sh), not root.
if [[ "${EUID}" -eq 0 ]]; then
  echo "Do not run this with sudo." >&2
  echo "It mirrors the invoking user's \$HOME and uses their SSH key under" >&2
  echo "\$HOME/.local/share/falltrap; under sudo both resolve to /root and fail." >&2
  echo "Re-run as the user whose fish session you intend to migrate:" >&2
  echo "  ./vm/debian/sync-session.sh" >&2
  exit 1
fi

source "$(dirname "$0")/falltrap-env.sh"

# --- Require a running guest and resolve its IP (same as shell.sh/start.sh) ---
state="$(virsh -c "${FT_CONNECT}" domstate "${FT_DOMAIN}" 2>/dev/null || echo missing)"
if [[ "${state}" != "running" ]]; then
  echo "Domain '${FT_DOMAIN}' is not running (state: ${state}). Start it first." >&2
  exit 1
fi

guest_ip="$(virsh -c "${FT_CONNECT}" -q domifaddr "${FT_DOMAIN}" \
            | awk '/ipv4/ {split($4, a, "/"); print a[1]; exit}')"
if [[ -z "${guest_ip}" ]]; then
  echo "Could not determine guest IP." >&2
  exit 1
fi

SSH_OPTS=(
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
  -o LogLevel=ERROR
  -i "${FT_SSH_KEY}"
)
remote="${FT_SSH_USER}@${guest_ip}"

# guest_run executes a command on the guest as FT_SSH_USER (has passwordless
# sudo per create.sh's cloud-init).
guest_run() { ssh "${SSH_OPTS[@]}" "${remote}" -- "$@"; }

# --- 1. Ensure fish is installed in the guest, then gate on binary identity ---
if ! command -v fish >/dev/null 2>&1; then
  echo "fish is not installed on this source host; nothing to migrate." >&2
  exit 1
fi
src_fish="$(command -v fish)"

echo "Ensuring fish is installed in the guest..."
# NETWORK (inside the guest): apt fetches the package. The human runs this, so
# it is not one of oubliette's own network calls.
guest_run 'command -v fish >/dev/null 2>&1 || { sudo apt-get update && sudo apt-get install -y fish; }'

echo "Verifying the fish binary is byte-identical (required for CRIU)..."
src_sum="$(sha256sum "${src_fish}" | awk '{print $1}')"
guest_fish="$(guest_run 'command -v fish')"
guest_sum="$(guest_run "sha256sum '${guest_fish}'" | awk '{print $1}')"
if [[ "${src_sum}" != "${guest_sum}" ]]; then
  echo >&2
  echo "ERROR: fish binaries differ between source and guest." >&2
  echo "  source: ${src_fish} (${src_sum})" >&2
  echo "  guest:  ${guest_fish} (${guest_sum})" >&2
  echo "CRIU restore of a file-backed mapping requires an identical binary." >&2
  echo "Install the exact same fish package version in the guest, or copy the" >&2
  echo "binary and its shared libraries verbatim, then re-run." >&2
  exit 1
fi
echo "  fish binary matches: ${src_sum}"

# --- 2. Mirror the user's home directory into the guest ---
# rsync flags: -a (perms/times/symlinks -- mode matters to CRIU), -H (hardlinks),
# -A -X (ACLs/xattrs) for fidelity, --delete so the guest home is an exact
# mirror. --info=progress2 gives a live total so a large copy doesn't look hung.
#
# Exclusions:
#   .ssh/       -- keep the guest's authorized_keys this script connects with;
#                  not part of the migrated session's open files anyway.
#   FT_WORKDIR  -- the VM working dir ($HOME/.local/share/falltrap): multi-GB
#                  qcow2 images, the virtiofs share, and the root-owned CRIU
#                  dump. It is VM infrastructure, not session state, and copying
#                  it would blow up the guest disk (and hit permission denials).
echo "Mirroring ${HOME}/ into ${remote}:/home/${FT_SSH_USER}/ ..."
if ! command -v rsync >/dev/null 2>&1; then
  echo "rsync missing on source host; install it and re-run." >&2
  exit 1
fi
guest_run 'command -v rsync >/dev/null 2>&1 || { sudo apt-get update && sudo apt-get install -y rsync; }'

# Path of the VM working dir relative to the rsync source ($HOME/), anchored.
ft_workdir_rel="${FT_WORKDIR#"${HOME}/"}"

rsync -aHAX --delete --info=progress2 \
  --exclude='.ssh/' \
  --exclude="/${ft_workdir_rel}/" \
  -e "ssh ${SSH_OPTS[*]}" \
  "${HOME}/" "${remote}:/home/${FT_SSH_USER}/"

echo
echo "Done. Guest '${FT_DOMAIN}' now mirrors this host's fish binary and home."
echo "Transient /run/user/${UID}/ FIFOs are handled at restore time, not here."
