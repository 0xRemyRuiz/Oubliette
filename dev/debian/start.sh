#!/usr/bin/env bash

bash $(dirname "$0")/../vm/debian/devhost/start.sh

bash $(dirname "$0")/../vm/debian/devhost/shell.sh -- ./src/vm/debian/start.sh
