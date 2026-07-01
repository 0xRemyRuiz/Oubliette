#!/usr/bin/env bash
# Shared config for the L1 devhost VM scripts. Source from each script.
# Runs under bash; your interactive fish shell doesn't affect these.

export DH_DOMAIN="devhost-debian"

export DH_WORKDIR="${HOME}/.local/share/falltrap-devhost"
export DH_IMAGE_DIR="${DH_WORKDIR}/images"
export DH_SEED_DIR="${DH_WORKDIR}/seed"

export DH_DISK="${DH_IMAGE_DIR}/${DH_DOMAIN}.qcow2"
# Base image is kept on destroy so re-create doesn't re-download.
export DH_CLOUD_IMG="${DH_IMAGE_DIR}/debian-12-generic-amd64.qcow2"

export DH_SSH_USER="falltrap"
export DH_SSH_KEY="${DH_WORKDIR}/id_ed25519_devhost"

export DH_CONNECT="qemu:///system"
