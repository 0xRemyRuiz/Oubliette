#!/usr/bin/env bash

sudo cat /root/oubliette_status.txt | grep "install ok" 2>&1 >/dev/null
if [[ $? -ne 0 ]]; then
  echo -e "\033[31mERROR:\033[0m System is not compatible" >&2
  exit 1
fi

echo "go build -o ../oubliette ./cmd/oubliette" &&\
go build -o ../oubliette ./cmd/oubliette &&\
echo "go build -o ../oubliette-restorehelper ./cmd/oubliette-restorehelper" &&\
go build -o ../oubliette-restorehelper ./cmd/oubliette-restorehelper
