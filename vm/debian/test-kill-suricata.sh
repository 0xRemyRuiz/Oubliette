#!/usr/bin/env bash

sudo systemctl stop suricata; sudo systemctl disable suricata 2>/dev/null; sudo pkill -x suricata; sleep 1; pgrep -a suricata || echo "No suricata running"
