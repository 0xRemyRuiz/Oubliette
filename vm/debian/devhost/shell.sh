#!/usr/bin/env bash
# Shell into the L1 devhost, or run a command / piped script on it.
#   ./shell.sh                # interactive
#   ./shell.sh -- uname -a    # one-off command
#   ./shell.sh < script.sh    # pipe a script to remote bash

set -euo pipefail
source "$(dirname "$0")/devhost-env.sh"

state="$(virsh -c "${DH_CONNECT}" domstate "${DH_DOMAIN}" 2>/dev/null || echo missing)"
if [[ "${state}" != "running" ]]; then
  echo "Domain '${DH_DOMAIN}' is not running (state: ${state})." >&2
  exit 1
fi

guest_ip="$(virsh -c "${DH_CONNECT}" -q domifaddr "${DH_DOMAIN}" \
            | awk '/ipv4/ {split($4, a, "/"); print a[1]; exit}')"
if [[ -z "${guest_ip}" ]]; then
  echo "Could not determine guest IP." >&2
  exit 1
fi

SSH_OPTS=(
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
  -o LogLevel=ERROR
  -i "${DH_SSH_KEY}"
)

if [[ $# -gt 0 ]]; then
  [[ "${1}" == "--" ]] && shift
  ssh "${SSH_OPTS[@]}" "${DH_SSH_USER}@${guest_ip}" -- "$@"
elif [[ ! -t 0 ]]; then
  ssh "${SSH_OPTS[@]}" "${DH_SSH_USER}@${guest_ip}" 'bash -s'
else
  ssh "${SSH_OPTS[@]}" -t "${DH_SSH_USER}@${guest_ip}"
fi