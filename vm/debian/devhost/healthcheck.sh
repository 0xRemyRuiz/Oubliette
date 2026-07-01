#!/usr/bin/env bash
# Verify the L1 devhost is in working order: defined, running, reachable,
# and the shared source mount is present inside it.

set -uo pipefail
source "$(dirname "$0")/devhost-env.sh"

pass() { printf "  \033[32mOK\033[0m   %s\n" "$1"; }
fail() { printf "  \033[31mFAIL\033[0m %s\n" "$1"; FAILED=1; }
info() { printf "  \033[34mINFO\033[0m %s\n" "$1"; }
FAILED=0

echo "== L0 host checks =="
command -v virsh >/dev/null        && pass "virsh present"      || fail "virsh missing"
command -v ssh   >/dev/null        && pass "ssh present"        || fail "ssh missing"
virsh -c "${DH_CONNECT}" uri >/dev/null 2>&1 \
                                   && pass "libvirt reachable"  || fail "libvirt unreachable (${DH_CONNECT})"

if virsh -c "${DH_CONNECT}" dominfo "${DH_DOMAIN}" >/dev/null 2>&1; then
  pass "domain '${DH_DOMAIN}' defined"
else
  fail "domain '${DH_DOMAIN}' not defined"
fi

[[ -f "${DH_SSH_KEY}" ]] && pass "ssh key present" || fail "ssh key missing: ${DH_SSH_KEY}"

echo
echo "== L1 guest checks =="
state="$(virsh -c "${DH_CONNECT}" domstate "${DH_DOMAIN}" 2>/dev/null || echo missing)"
if [[ "${state}" != "running" ]]; then
  info "guest not running (state: ${state}); start it to run guest checks"
  [[ "${FAILED}" -eq 1 ]] && exit 1 || exit 0
fi

guest_ip="$(virsh -c "${DH_CONNECT}" -q domifaddr "${DH_DOMAIN}" \
            | awk '/ipv4/ {split($4, a, "/"); print a[1]; exit}')"
if [[ -z "${guest_ip}" ]]; then
  fail "could not determine guest IP"
  exit 1
fi
pass "guest IP: ${guest_ip}"

report="$(ssh -q -o BatchMode=yes -o StrictHostKeyChecking=no \
              -o UserKnownHostsFile=/dev/null -o ConnectTimeout=4 \
              -i "${DH_SSH_KEY}" "${DH_SSH_USER}@${guest_ip}" 'bash -s' <<'REMOTE' 2>/dev/null
echo "kernel:$(uname -r)"
echo "src_mount:$(mountpoint -q /home/'"${USER}"'/src && echo ok || echo missing)"
REMOTE
)" || { fail "SSH command failed"; exit 1; }

# The heredoc above can't see host vars, so re-check the mount plainly:
report="$(ssh -q -o BatchMode=yes -o StrictHostKeyChecking=no \
              -o UserKnownHostsFile=/dev/null -o ConnectTimeout=4 \
              -i "${DH_SSH_KEY}" "${DH_SSH_USER}@${guest_ip}" \
              "echo kernel:\$(uname -r); mountpoint -q \$HOME/src && echo src_mount:ok || echo src_mount:missing" 2>/dev/null)"

get() { echo "${report}" | awk -F: -v k="$1" '$1==k {print $2}'; }

[[ -n "$(get kernel)" ]]            && pass "guest kernel: $(get kernel)"      || fail "could not read guest kernel"
[[ "$(get src_mount)" == "ok" ]]    && pass "source share mounted at ~/src"    || fail "source share not mounted at ~/src"

echo
if [[ "${FAILED}" -eq 0 ]]; then
  echo "Devhost L1 looks healthy."
else
  echo "One or more checks failed."
  exit 1
fi