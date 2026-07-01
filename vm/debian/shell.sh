#!/usr/bin/env bash
# Open an interactive shell inside the running VM via SSH,
# or run a one-off command / script if arguments are given.
#
# Usage:
#   ./shell.sh                 # interactive shell
#   ./shell.sh -- uname -a     # run command on guest
#   ./shell.sh < script.sh     # pipe script to guest bash

set -euo pipefail
source "$(dirname "$0")/falltrap-env.sh"

state="$(virsh domstate "${FT_DOMAIN}" 2>/dev/null || echo "missing")"
if [[ "${state}" != "running" ]]; then
  echo "Domain '${FT_DOMAIN}' is not running (state: ${state})." >&2
  exit 1
fi

guest_ip="$(virsh -q domifaddr "${FT_DOMAIN}" \
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

if [[ $# -gt 0 ]]; then
  # Explicit command mode: everything after `--` is the remote command.
  if [[ "${1}" == "--" ]]; then shift; fi
  ssh "${SSH_OPTS[@]}" "${FT_SSH_USER}@${guest_ip}" -- "$@"
elif [[ ! -t 0 ]]; then
  # stdin is piped: feed it to a remote bash.
  ssh "${SSH_OPTS[@]}" "${FT_SSH_USER}@${guest_ip}" 'bash -s'
else
  # Interactive shell.
  ssh "${SSH_OPTS[@]}" -t "${FT_SSH_USER}@${guest_ip}"
fi