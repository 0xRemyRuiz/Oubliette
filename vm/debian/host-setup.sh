#!/bin/sh

# Debian/Ubuntu host
sudo apt-get update -y &&\
sudo apt-get install -y \
  qemu-system-x86 qemu-utils \
  libvirt-daemon-system libvirt-clients \
  virtinst virt-manager \
  cloud-image-utils \
  criu \
  rsync \
  genisoimage &&\
\
sudo usermod -aG libvirt "$USER" &&\
\
systemctl status libvirtd --no-pager &&\
\
if sudo virsh net-info default 2>/dev/null | grep -q "^Active:.*yes"; then
  :
elif ! sudo virsh net-start default 2>/dev/null; then
  # Fresh libvirt installs ship a "default" NAT network hardcoded to
  # 192.168.122.0/24. If this host is itself a VM whose own uplink NIC
  # is already on that subnet (nested libvirt, e.g. running inside the
  # devhost L1 box), starting a second network on the same range
  # collides with the existing interface. Rebind to a disjoint subnet
  # and retry, instead of silently leaving the network inactive.
  echo "Default network collided with an existing interface; rebinding to 192.168.200.0/24..." >&2
  cat > /tmp/oubliette-default-net.xml <<NETXML
<network>
  <name>default</name>
  <bridge name='virbr0'/>
  <forward mode='nat'/>
  <ip address='192.168.200.1' netmask='255.255.255.0'>
    <dhcp>
      <range start='192.168.200.2' end='192.168.200.254'/>
    </dhcp>
  </ip>
</network>
NETXML
  sudo virsh net-destroy default >/dev/null 2>&1
  sudo virsh net-undefine default >/dev/null 2>&1
  sudo virsh net-define /tmp/oubliette-default-net.xml &&\
  sudo virsh net-start default
fi &&\
sudo virsh net-autostart default &&\
\
echo "install ok" | sudo tee "/root/oubliette_status.txt" &&\
echo "SUCCESS: Host is setup and ready to integrate the falltrap" &&\
exit 0

echo "WARNING: Host base setup failed!"
exit 1
