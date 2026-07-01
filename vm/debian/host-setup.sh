#!/bin/sh

# Debian/Ubuntu host
sudo apt update
sudo apt install -y \
  qemu-system-x86 qemu-utils \
  libvirt-daemon-system libvirt-clients \
  virtinst virt-manager \
  cloud-image-utils \
  criu \
  genisoimage

# Add yourself to the libvirt group so you don't need sudo for virsh
sudo usermod -aG libvirt "$USER"
# Log out / back in for the group change to take effect

# Sanity: libvirt running?
systemctl status libvirtd --no-pager
