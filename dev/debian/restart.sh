#!/usr/bin/env bash

bash $(dirname "$0")/stop-debian.sh &&\
bash $(dirname "$0")/start-debian.sh
