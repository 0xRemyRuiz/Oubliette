#!/usr/bin/env bash
# Provision the falltrap research VM from a Debian cloud image.
# Idempotent-ish: refuses to run if the domain already exists.
# Contains network requests: downloads the cloud image if absent.

set -euo pipefail
source "$(dirname "$0")/falltrap-env.sh"

# --- Refuse if the domain already exists ---
if virsh -c qemu:///system dominfo "${FT_DOMAIN}" >/dev/null 2>&1; then
  echo "Domain '${FT_DOMAIN}' already exists. Destroy it first with $(dirname "$0")/destroy.sh." >&2
  exit 1
fi

# --- Fetch base image if missing (NETWORK) ---
if [[ ! -f "${FT_CLOUD_IMG}" ]]; then
  echo "Fetching Debian 12 cloud image..."
  curl -fSL -o "${FT_CLOUD_IMG}" "${FT_CLOUD_IMG_URL}"
fi

# --- Make a per-domain overlay disk so we don't dirty the base ---
cp --reflink=auto "${FT_CLOUD_IMG}" "${FT_DISK}"
qemu-img resize "${FT_DISK}" "${FT_DISK_SIZE}"

# --- Generate an SSH key for the research user if missing ---
if [[ ! -f "${FT_SSH_KEY}" ]]; then
  ssh-keygen -t ed25519 -N "" -f "${FT_SSH_KEY}" -C "falltrap-research"
fi
SSH_PUBKEY="$(cat "${FT_SSH_KEY}.pub")"

# --- Build cloud-init seed (user-data + meta-data) ---
cat > "${FT_SEED_DIR}/user-data" <<EOF
#cloud-config
hostname: ${FT_DOMAIN}
fqdn: ${FT_DOMAIN}.local
manage_etc_hosts: true

users:
  - name: ${FT_SSH_USER}
    groups: sudo
    shell: /bin/bash
    sudo: 'ALL=(ALL) NOPASSWD:ALL'
    ssh_authorized_keys:
      - ${SSH_PUBKEY}

# Install CRIU and a few things we need on the guest side.
package_update: true
packages:
  - criu
  - iproute2
  - openssh-server
  - qemu-guest-agent

# Ensure the shared virtiofs mount point exists at first boot.
runcmd:
  - mkdir -p /mnt/falltrap-shared
  - echo "falltrap-shared /mnt/falltrap-shared virtiofs defaults 0 0" >> /etc/fstab
  - mount -a || true
  # qemu-guest-agent is udev-activated: its service is started when the
  # virtio-ports device appears. That device shows up at boot, *before*
  # cloud-init installs the package here, so the activation event is missed
  # on this first boot. Later boots are handled by udev; kick it once now so
  # the agent is reachable without requiring a reboot.
  - systemctl start qemu-guest-agent
  # Interactive shells (fish here) keep per-user runtime state -- FIFOs,
  # sockets -- under XDG_RUNTIME_DIR (/run/user/<uid>). systemd-logind only
  # creates that directory while the user has a login session, but the restore
  # helper runs via the guest agent with no session, so criu restore would
  # fail to recreate those fds ("No such file or directory" under
  # /run/user/<uid>). Enable lingering so /run/user/<uid> exists at boot,
  # independent of logins, giving criu a coherent runtime dir to restore into.
  - loginctl enable-linger ${FT_SSH_USER}
EOF

cat > "${FT_SEED_DIR}/meta-data" <<EOF
instance-id: ${FT_DOMAIN}
local-hostname: ${FT_DOMAIN}
EOF

SEED_ISO="${FT_SEED_DIR}/seed.iso"
genisoimage -output "${SEED_ISO}" -volid cidata -joliet -rock \
  "${FT_SEED_DIR}/user-data" "${FT_SEED_DIR}/meta-data" >/dev/null 2>&1

# --- Define the domain via virt-install ---
# --noreboot + --noautoconsole keep this scriptable.
# --memorybacking source.type=memfd is required for virtiofs.
# vsock is exposed so we can use it as the PTY/control channel later.
virt-install \
  --connect ${FT_CONNECT} \
  --name "${FT_DOMAIN}" \
  --memory "${FT_RAM_MB}" \
  --vcpus "${FT_VCPUS}" \
  --cpu host-passthrough \
  --osinfo detect=on,require=off \
  --disk path="${FT_DISK}",format=qcow2,bus=virtio \
  --disk path="${SEED_ISO}",device=cdrom \
  --network network=default,model=virtio \
  --graphics none \
  --console pty,target_type=serial \
  --channel unix,target_type=virtio,name=org.qemu.guest_agent.0 \
  --memorybacking source.type=memfd,access.mode=shared \
  --filesystem "${FT_SHARED_DIR},falltrap-shared,driver.type=virtiofs" \
  --vsock cid.address="${FT_VSOCK_CID}" \
  --import \
  --noautoconsole \
  --noreboot

echo
echo "Domain '${FT_DOMAIN}' defined."
echo "Start it with: $(dirname "$0")/start.sh"