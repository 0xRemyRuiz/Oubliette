#!/usr/bin/env bash
# Start the falltrap VM and wait until SSH is reachable.
# Prints the domain name on stdout on success (so callers can capture it).

set -euo pipefail
source "$(dirname "$0")/falltrap-env.sh"

# --- Ensure the domain is defined ---
if ! virsh dominfo "${FT_DOMAIN}" >/dev/null 2>&1; then
  echo "Domain '${FT_DOMAIN}' not defined. Run falltrap-vm-create.sh first." >&2
  exit 1
fi

# --- Start if not already running ---
state="$(virsh domstate "${FT_DOMAIN}")"
if [[ "${state}" != "running" ]]; then
  virsh start "${FT_DOMAIN}" >/dev/null
fi

# --- Wait for a DHCP lease on the default libvirt network ---
echo "Waiting for guest IP..." >&2
guest_ip=""
for _ in $(seq 1 60); do
  guest_ip="$(virsh -q domifaddr "${FT_DOMAIN}" \
              | awk '/ipv4/ {split($4, a, "/"); print a[1]; exit}')"
  [[ -n "${guest_ip}" ]] && break
  sleep 2
done

if [[ -z "${guest_ip}" ]]; then
  echo "Timed out waiting for guest IP." >&2
  exit 1
fi

# --- Wait for SSH ---
echo "Waiting for SSH on ${guest_ip}..." >&2
for _ in $(seq 1 60); do
  if ssh -q -o BatchMode=yes -o StrictHostKeyChecking=no \
         -o UserKnownHostsFile=/dev/null \
         -o ConnectTimeout=2 \
         -i "${FT_SSH_KEY}" \
         "${FT_SSH_USER}@${guest_ip}" \
         true 2>/dev/null; then
    break
  fi
  sleep 2
done

# --- Report ---
echo "${FT_DOMAIN}"
echo "IP: ${guest_ip}" >&2