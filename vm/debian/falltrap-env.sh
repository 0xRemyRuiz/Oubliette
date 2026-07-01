#!/usr/bin/env bash
# Shared environment for falltrap research VM scripts.
# Source this from every other script: `source ./falltrap-env.sh`

echo "Checking system compatibility..."
sudo cat /root/oubliette_status.txt | grep "install ok"
if [[ $? -ne 0 ]]; then
  echo -e "\033[31mERROR:\033[0m System is not compatible" >&2
  exit 1
fi

# Name of the libvirt domain. Deterministic so scripts can find it.
export FT_DOMAIN="falltrap-debian"
export FT_CONNECT="qemu:///system"

# Where VM artifacts live on the host.
export FT_WORKDIR="${HOME}/.local/share/falltrap"
export FT_IMAGE_DIR="${FT_WORKDIR}/images"
export FT_SEED_DIR="${FT_WORKDIR}/seed"
export FT_SHARED_DIR="${FT_WORKDIR}/shared"   # virtiofs mount, checkpoint transport

# Debian cloud image (bookworm, generic amd64).
# NOTE: downloading this is a network request.
export FT_CLOUD_IMG_URL="https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2"
export FT_CLOUD_IMG="${FT_IMAGE_DIR}/debian-12-generic-amd64.qcow2"
export FT_DISK="${FT_IMAGE_DIR}/${FT_DOMAIN}.qcow2"
export FT_DISK_SIZE="10G"

# VM resources. Keep small for research.
export FT_VCPUS="2"
export FT_RAM_MB="2048"

# vsock CID. Any unique small integer >=3. Used later for PTY channel.
export FT_VSOCK_CID="42"

# SSH access to the guest during research.
export FT_SSH_USER="falltrap"
export FT_SSH_KEY="${FT_WORKDIR}/id_ed25519_falltrap"

mkdir -p "${FT_IMAGE_DIR}" "${FT_SEED_DIR}" "${FT_SHARED_DIR}"

# The hypervisor runs qemu as its own unprivileged user (commonly
# libvirt-qemu), which needs search (x) permission on every directory
# down to the disk image/ISO paths above -- not just read/write for the
# owning user. mkdir -p only sets modes on dirs it actually creates, so
# on hosts where ~/.local or ~/.local/share already existed with a
# restrictive umask (e.g. 700), that traversal is missing. Grant it
# explicitly so libvirt doesn't warn about inaccessible disk images.
chmod o+x "${HOME}/.local" "${HOME}/.local/share" "${FT_WORKDIR}" \
  "${FT_IMAGE_DIR}" "${FT_SEED_DIR}" "${FT_SHARED_DIR}" 2>/dev/null || true