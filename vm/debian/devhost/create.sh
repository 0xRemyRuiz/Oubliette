#!/usr/bin/env bash
# create.sh
#
# Creates a plain Debian 12 VM (L1) for development, with the project source
# shared in over 9p.
#
# Scope: creates the L1 VM, then runs ../host-setup.sh *inside* it once it
# comes up, so L1 is provisioned (qemu/libvirt/criu via apt, inside the
# guest) and ready to host the L2 shadow VM. L0 itself is untouched.
#
# Contains network requests: downloads the Debian cloud image if absent
# (on L0), and host-setup.sh does its own apt installs (inside L1).
# Runs under bash regardless of your interactive shell being fish.

set -euo pipefail
source "$(dirname "$0")/devhost-env.sh"

# L0 source dir to share into L1. Override via FALLTRAP_SRC.
DH_SOURCE_DIR="${FALLTRAP_SRC:-$(pwd)}"

DH_CLOUD_IMG_URL="https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2"
DH_DISK_SIZE="20G"

DH_VCPUS="2"
DH_RAM_MB="2048"

DH_SRC_TAG="falltrapsrc"   # 9p mount tag
# -----------------------------------------------------------------------

mkdir -p "${DH_IMAGE_DIR}" "${DH_SEED_DIR}"

# --- Preconditions on L0 ---
if ! command -v virt-install >/dev/null; then
  echo "virt-install not found" >&2
  exit 1
fi
if ! command -v genisoimage >/dev/null && ! command -v xorriso >/dev/null; then
  echo "Need genisoimage or xorriso" >&2
  exit 1
fi
if ! virsh -c qemu:///system uri >/dev/null 2>&1; then
  echo "Cannot reach qemu:///system. Try:" >&2
  echo "  sudo systemctl enable --now libvirtd.socket" >&2
  echo "  sudo usermod -aG libvirt \$USER   # then re-login" >&2
  exit 1
fi
if [[ ! -d "${DH_SOURCE_DIR}" ]]; then
  echo "Source dir '${DH_SOURCE_DIR}' does not exist. Create it or set FALLTRAP_SRC." >&2
  exit 1
fi
if virsh -c qemu:///system dominfo "${DH_DOMAIN}" >/dev/null 2>&1; then
  echo "Domain '${DH_DOMAIN}' already exists. Destroy it first." >&2
  exit 1
fi

# --- Fetch base image if missing (NETWORK) ---
if [[ ! -f "${DH_CLOUD_IMG}" ]]; then
  echo "Fetching Debian 12 cloud image (network)..."
  curl -fSL -o "${DH_CLOUD_IMG}" "${DH_CLOUD_IMG_URL}"
fi

# --- Overlay disk from base ---
cp --reflink=auto "${DH_CLOUD_IMG}" "${DH_DISK}"
qemu-img resize "${DH_DISK}" "${DH_DISK_SIZE}"

# --- SSH key ---
if [[ ! -f "${DH_SSH_KEY}" ]]; then
  ssh-keygen -t ed25519 -N "" -f "${DH_SSH_KEY}" -C "falltrap-devhost"
fi
SSH_PUBKEY="$(cat "${DH_SSH_KEY}.pub")"

# --- cloud-init seed: a plain dev box + the 9p source mount ---
cat > "${DH_SEED_DIR}/user-data" <<EOF
#cloud-config
hostname: ${DH_DOMAIN}
fqdn: ${DH_DOMAIN}.local
manage_etc_hosts: true

users:
  - name: ${DH_SSH_USER}
    groups: [sudo]
    shell: /bin/bash
    sudo: 'ALL=(ALL) NOPASSWD:ALL'
    ssh_authorized_keys:
      - ${SSH_PUBKEY}

package_update: true
packages:
  - openssh-server
  - git

mounts:
  - [ "${DH_SRC_TAG}", "/home/${DH_SSH_USER}/src", "9p", "trans=virtio,version=9p2000.L,msize=524288,rw,_netdev", "0", "0" ]

runcmd:
  # The "mounts" cloud-init module runs before "users-groups", so it
  # mkdir -p's /home/${DH_SSH_USER} (as root) to create the 9p mount
  # point before the user's home dir exists; useradd then leaves the
  # home dir itself root-owned since it already exists. Fix it here.
  - chown ${DH_SSH_USER}:${DH_SSH_USER} /home/${DH_SSH_USER}
  - mkdir -p /home/${DH_SSH_USER}/src
  - chown ${DH_SSH_USER}:${DH_SSH_USER} /home/${DH_SSH_USER}/src
  - mount -a || true
EOF

cat > "${DH_SEED_DIR}/meta-data" <<EOF
instance-id: ${DH_DOMAIN}
local-hostname: ${DH_DOMAIN}
EOF

SEED_ISO="${DH_SEED_DIR}/seed.iso"
if command -v genisoimage >/dev/null; then
  genisoimage -output "${SEED_ISO}" -volid cidata -joliet -rock \
    "${DH_SEED_DIR}/user-data" "${DH_SEED_DIR}/meta-data" >/dev/null 2>&1
else
  xorriso -as genisoimage -output "${SEED_ISO}" -volid cidata -joliet -rock \
    "${DH_SEED_DIR}/user-data" "${DH_SEED_DIR}/meta-data" >/dev/null 2>&1
fi

# --- Define + start L1 ---
virt-install \
  --connect ${DH_CONNECT} \
  --name "${DH_DOMAIN}" \
  --memory "${DH_RAM_MB}" \
  --vcpus "${DH_VCPUS}" \
  --cpu host-passthrough \
  --os-variant debian12 \
  --disk path="${DH_DISK}",format=qcow2,bus=virtio \
  --disk path="${SEED_ISO}",device=cdrom \
  --network network=default,model=virtio \
  --filesystem "${DH_SOURCE_DIR},${DH_SRC_TAG},driver.type=path,accessmode=mapped" \
  --graphics none \
  --console pty,target_type=serial \
  --import \
  --noautoconsole

echo
echo "L1 domain '${DH_DOMAIN}' created and starting."

# --- Wait for L1 to come up, then provision it from the inside ---
# start.sh already knows how to wait for a guest IP + SSH; reuse it.
"$(dirname "$0")/start.sh" >/dev/null

# SSH answers before cloud-init's own package install (openssh-server,
# git) finishes running in the background. Wait it out, otherwise our
# apt-get below races cloud-init's apt-get for the dpkg lock.
echo "Waiting for cloud-init to finish inside L1..."
if ! "$(dirname "$0")/shell.sh" -- cloud-init status --wait >/dev/null; then
  echo "cloud-init did not finish cleanly inside L1 '${DH_DOMAIN}'." >&2
  exit 1
fi

echo "Running prettifish.sh inside L1 (just a better prompt)..."
if ! "$(dirname "$0")/shell.sh" < "$(dirname "$0")/prettifish.sh"; then
  echo "prettifish.sh failed inside L1 '${DH_DOMAIN}'." >&2
fi

echo "Running host-setup.sh inside L1 (network request: apt installs)..."
if ! "$(dirname "$0")/shell.sh" < "$(dirname "$0")/../host-setup.sh"; then
  echo "host-setup.sh failed inside L1 '${DH_DOMAIN}'." >&2
  exit 1
fi

echo
echo "L1 domain '${DH_DOMAIN}' provisioned and ready."
echo "Find its IP: virsh -c qemu:///system domifaddr ${DH_DOMAIN}"
echo "SSH in:      ssh -i ${DH_SSH_KEY} -o StrictHostKeyChecking=no ${DH_SSH_USER}@<ip>"
echo "Your source is mounted inside L1 at /home/${DH_SSH_USER}/src"