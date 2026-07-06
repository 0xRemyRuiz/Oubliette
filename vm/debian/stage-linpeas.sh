#!/usr/bin/env bash
# Stage tests/linpeas.sh at an identical path on both this host and the shadow
# VM, so a shell running it can be migrated mid-run.
#
# Why: a shell executing a script keeps the script file open and reads commands
# from it. CRIU dumps that open fd; restore reopens it at the same path in the
# guest. If the script is not present there, restore fails and the migrated
# tree dies. Staging it on both sides closes that coherence gap.
#
# Target path is /tmp/linpeas.sh
# (what the broker spawns, and what the manual test below uses).

set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "${HERE}/../.." && pwd)"

SRC="${LINPEAS_SRC:-${REPO}/tests/linpeas.sh}"
DEST="/tmp/linpeas.sh"

if [[ ! -f "${SRC}" ]]; then
  echo "linpeas not found at ${SRC}; download it into tests/ first." >&2
  exit 1
fi

# --- Local host: where the shell running linpeas lives (root, cwd /root) ---
sudo install -m 0755 "${SRC}" "${DEST}"
echo "staged ${DEST} (local)"

# --- Shadow VM: same path, so the restored shell's open script fd resolves ---
"${HERE}/shell.sh" -- sh -c "sudo tee ${DEST} >/dev/null && sudo chmod 0755 ${DEST}" < "${SRC}"
echo "staged ${DEST} (guest)"

echo
echo "Both sides staged. The shell that runs ${DEST} can now migrate mid-run."
