#!/usr/bin/env bash
# Verify that everything needed for a falltrap CRIU round-trip is in place,
# on both host and guest. Non-zero exit if any check fails.

set -uo pipefail
source "$(dirname "$0")/falltrap-env.sh"

pass() { printf "  \033[32mOK\033[0m   %s\n" "$1"; }
fail() { printf "  \033[31mFAIL\033[0m %s\n" "$1"; FAILED=1; }
info() { printf "  \033[34mINFO\033[0m %s\n" "$1"; }

FAILED=0

echo "== Host checks =="
command -v virsh >/dev/null      && pass "virsh present"        || fail "virsh missing"
command -v virt-install >/dev/null && pass "virt-install present" || fail "virt-install missing"
command -v criu >/dev/null       && pass "criu present"          || fail "criu missing"
command -v qemu-img >/dev/null   && pass "qemu-img present"      || fail "qemu-img missing"
systemctl is-active --quiet libvirtd && pass "libvirtd running"  || fail "libvirtd not running"

if virsh dominfo "${FT_DOMAIN}" >/dev/null 2>&1; then
  pass "domain '${FT_DOMAIN}' defined"
else
  fail "domain '${FT_DOMAIN}' not defined"
fi

if [[ -d "${FT_SHARED_DIR}" ]]; then
  pass "shared dir exists on host: ${FT_SHARED_DIR}"
else
  fail "shared dir missing on host: ${FT_SHARED_DIR}"
fi

echo
echo "== Guest checks =="
state="$(virsh domstate "${FT_DOMAIN}" 2>/dev/null || echo missing)"
if [[ "${state}" != "running" ]]; then
  info "guest not running (state: ${state}); skipping guest checks"
  [[ "${FAILED}" -eq 1 ]] && exit 1 || exit 0
fi

# Run all guest checks in a single SSH call for speed.
guest_report="$(./shell.sh <<'REMOTE'
set -u
echo "kernel:$(uname -r)"
echo "criu:$(command -v criu || echo missing)"
if command -v criu >/dev/null; then
  # `criu check` exits nonzero if the kernel lacks required features.
  if criu check >/dev/null 2>&1; then
    echo "criu_check:ok"
  else
    echo "criu_check:fail"
  fi
fi
echo "shared_mount:$(mountpoint -q /mnt/falltrap-shared && echo ok || echo missing)"
echo "vsock:$(test -c /dev/vsock && echo ok || echo missing)"
REMOTE
)"

get() { echo "${guest_report}" | awk -F: -v k="$1" '$1==k {print $2}'; }

[[ -n "$(get kernel)" ]]              && pass "guest kernel: $(get kernel)" || fail "could not read guest kernel"
[[ "$(get criu)" != "missing" ]]      && pass "criu on guest: $(get criu)"  || fail "criu missing on guest"
[[ "$(get criu_check)" == "ok" ]]     && pass "criu check on guest"          || fail "criu check failed on guest"
[[ "$(get shared_mount)" == "ok" ]]   && pass "virtiofs shared mount on guest" || fail "virtiofs shared mount missing on guest"
[[ "$(get vsock)" == "ok" ]]          && pass "vsock device on guest"        || fail "vsock device missing on guest"

echo
if [[ "${FAILED}" -eq 0 ]]; then
  echo "All checks passed. Ready to attempt CRIU round-trips."
else
  echo "One or more checks failed."
  exit 1
fi