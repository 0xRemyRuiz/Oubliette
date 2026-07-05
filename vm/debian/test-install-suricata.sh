#!/usr/bin/env bash
# Install and set up Suricata (IDS mode) on Debian 12 so it can feed
# containment triggers to oubliette's control socket.
#
# Suricata runs here as a *passive* IDS: it does not block traffic. It watches
# for signatures of interest and emits EVE JSON alerts; a small bridge (printed
# at the end) translates each alert into a `contain` request on oubliette's
# control socket, dropping the offending session into the shadow VM.
#
# CONTAINS NETWORK REQUESTS: apt-get update/install and suricata-update fetch
# packages and rules from the internet. Run it on the host where the broker
# (`oubliette serve`) runs.
#
# Usage:
#   sudo ./vm/debian/test-install-suricata.sh
#   sudo BROKER_PORT=2222 ./vm/debian/test-install-suricata.sh   # override port

set -euo pipefail

BROKER_PORT="${BROKER_PORT:-2222}"
CONTROL_SOCK="${CONTROL_SOCK:-/run/oubliette.sock}"
RULE_FILE=/etc/suricata/rules/oubliette-test.rules
EVE_LOG=/var/log/suricata/eve.json

# --- Require root (apt + writing under /etc/suricata) ---
if [[ "${EUID}" -ne 0 ]]; then
  echo "Run as root: sudo $0" >&2
  exit 1
fi

# --- Sanity: warn if this isn't Debian 12 ---
if [[ -r /etc/os-release ]]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  if [[ "${ID:-}" != "debian" ]]; then
    echo "Warning: this script targets Debian; detected ID=${ID:-unknown}." >&2
  elif [[ "${VERSION_ID:-}" != "12" ]]; then
    echo "Warning: this script targets Debian 12 (bookworm); detected ${VERSION_ID:-unknown}." >&2
  fi
fi

# --- Enable bookworm-backports ---
# Debian 12's main archive resolves suricata to a version that wants a newer
# libhtp2 than stable ships, so a plain `apt install suricata` breaks with an
# unmet libhtp2 dependency. Installing suricata (and its deps) from backports as
# one coherent set with `-t bookworm-backports` avoids the version mix.
export DEBIAN_FRONTEND=noninteractive
if ! grep -rqs "bookworm-backports" /etc/apt/sources.list /etc/apt/sources.list.d/ 2>/dev/null; then
  echo "deb http://deb.debian.org/debian bookworm-backports main" > /etc/apt/sources.list.d/backports.list
  echo "Enabled bookworm-backports."
fi
apt-get update

# Bridge tools come from stable; suricata is pinned to backports.
apt-get install -y jq socat
apt-get install -y -t bookworm-backports suricata

# suricata-update: pull the matching tool (name varies across releases).
if ! command -v suricata-update >/dev/null 2>&1; then
  apt-get install -y -t bookworm-backports suricata-update \
    || apt-get install -y suricata-update \
    || apt-get install -y python3-suricata-update \
    || echo "Warning: suricata-update could not be installed; fetch rules manually." >&2
fi

# --- The package auto-starts a service bound to a configured NIC. Stop AND
#     disable it: a running instance holds the af-packet cluster-id, which makes
#     a second (manual) instance fail with "cluster-id 1 already in use". We run
#     Suricata by hand against the interface we want for testing. ---
systemctl stop suricata 2>/dev/null || true
systemctl disable suricata 2>/dev/null || true

# --- Fetch the ET Open ruleset (populates /var/lib/suricata/rules) ---
if command -v suricata-update >/dev/null 2>&1; then
  suricata-update || echo "suricata-update failed (no network?); continuing with existing rules." >&2
else
  echo "suricata-update not available; skipping ruleset fetch." >&2
fi

# --- Custom test rule: alert when 'linpeas' appears in a session to the broker.
#     sid 9000000+ is a private/local range. This is what makes the end-to-end
#     Suricata -> contain path testable without a real attacker tool. ---
mkdir -p "$(dirname "${RULE_FILE}")"
cat > "${RULE_FILE}" <<EOF
# oubliette test rule: alert on 'linpeas' seen in traffic to the broker port.
alert tcp any any -> any ${BROKER_PORT} (msg:"OUBLIETTE trap: linpeas in broker session"; content:"linpeas"; nocase; sid:9000001; rev:1;)
EOF
echo "Wrote test rule to ${RULE_FILE}"

# --- Validate the configuration + rules ---
if suricata -T -c /etc/suricata/suricata.yaml -s "${RULE_FILE}" >/dev/null 2>&1; then
  echo "Suricata config test: OK"
else
  echo "Suricata config test reported issues; inspect with:" >&2
  echo "  sudo suricata -T -c /etc/suricata/suricata.yaml -s ${RULE_FILE}" >&2
fi

# --- Next steps ---
cat <<EOF

Suricata installed and set up.

1) Run Suricata in IDS mode. For a local loopback (nc) test:
     sudo suricata -i lo -k none --runmode single -S ${RULE_FILE} -l /var/log/suricata
   On loopback, -k none (skip checksums, which are absent on lo) and
   --runmode single (avoid the af-packet fanout cluster) are both required, or
   you get no alerts. On a real NIC, drop --runmode single and use -s
   (lowercase) to add this rule alongside the ET ruleset from suricata-update:
     sudo suricata -i eth0 -s ${RULE_FILE} -l /var/log/suricata
   (find your NIC with: ip -o link show)

2) Start the broker with the control socket enabled:
     sudo ../oubliette serve --listen :${BROKER_PORT} --control ${CONTROL_SOCK} --vm falltrap-debian

3) Bridge Suricata alerts -> oubliette containment (leave running):
     tail -F ${EVE_LOG} \\
       | jq -c --unbuffered 'select(.event_type=="alert" and .dest_port==${BROKER_PORT})
           | {action:"contain", match:{remote_ip:.src_ip, remote_port:.src_port}}' \\
       | while read -r line; do printf '%s\\n' "\$line" | sudo socat - UNIX-CONNECT:${CONTROL_SOCK}; done

4) Connect as the "attacker" and trip the rule:
     nc localhost ${BROKER_PORT}
     # then run something containing 'linpeas' -- Suricata alerts, the bridge
     # sends a contain request, and the session falls into the shadow VM.
EOF
