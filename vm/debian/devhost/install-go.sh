#!/usr/bin/env bash

cd /tmp &&\
curl -fSLO https://go.dev/dl/go1.26.4.linux-amd64.tar.gz &&\
sudo rm -rf /usr/local/go &&\
sudo tar -C /usr/local -xzf go1.26.4.linux-amd64.tar.gz &&\
echo "fish_add_path /usr/local/go/bin" >> ~/.config/fish/config.fish
