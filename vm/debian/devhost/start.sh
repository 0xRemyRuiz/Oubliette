#!/usr/bin/env bash
# Start the L1 devhost VM and wait until SSH answers.
# Prints the domain name on stdout; progress goes to stderr.

set -euo pipefail
source "$(dirname "$0")/devhost-env.sh"

if ! virsh -c "${DH_CONNECT}" dominfo "${DH_DOMAIN}" >/dev/null 2>&1; then
  echo "Domain '${DH_DOMAIN}' not defined. Run the create script first." >&2
  exit 1
fi

state="$(virsh -c "${DH_CONNECT}" domstate "${DH_DOMAIN}")"
if [[ "${state}" != "running" ]]; then
  virsh -c "${DH_CONNECT}" start "${DH_DOMAIN}" >/dev/null
fi

echo "Waiting for guest IP..." >&2
guest_ip=""
for _ in $(seq 1 60); do
  guest_ip="$(virsh -c "${DH_CONNECT}" -q domifaddr "${DH_DOMAIN}" \
              | awk '/ipv4/ {split($4, a, "/"); print a[1]; exit}')"
  [[ -n "${guest_ip}" ]] && break
  sleep 2
done
if [[ -z "${guest_ip}" ]]; then
  echo "Timed out waiting for guest IP." >&2
  exit 1
fi

echo "Waiting for SSH on ${guest_ip}..." >&2
for _ in $(seq 1 60); do
  if ssh -q -o BatchMode=yes -o StrictHostKeyChecking=no \
         -o UserKnownHostsFile=/dev/null -o ConnectTimeout=2 \
         -i "${DH_SSH_KEY}" "${DH_SSH_USER}@${guest_ip}" true 2>/dev/null; then
    break
  fi
  sleep 2
done

echo "${DH_DOMAIN}"
echo "IP: ${guest_ip}" >&2