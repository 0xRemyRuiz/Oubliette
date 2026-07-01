#!/bin/sh

# Debian/Ubuntu host
sudo apt-get update -y &&\
sudo apt-get install -y \
  qemu-system-x86 qemu-utils \
  libvirt-daemon-system libvirt-clients \
  virtinst virt-manager \
  cloud-image-utils \
  criu \
  genisoimage &&\
\
sudo usermod -aG libvirt "$USER" &&\
\
systemctl status libvirtd --no-pager &&\
\
echo "install ok" | sudo tee "/root/oubliette_status.txt" &&\
echo "SUCCESS: Host is setup and ready to integrate the falltrap" &&\
exit 0

echo "WARNING: Host base setup failed!"
exit 1
