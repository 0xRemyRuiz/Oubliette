#!/usr/bin/env bash

sudo setfacl -R -m u:libvirt-qemu:rwx $(pwd) &&\
sudo setfacl -R -d -m u:libvirt-qemu:rwx $(pwd) &&\
sudo chown $(whoami):$(whoami) -R $(pwd)
