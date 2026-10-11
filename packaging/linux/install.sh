#!/bin/sh
# Bond007 installer for any Linux with systemd (or OpenWrt).
#   sudo ./install.sh [--server host:7007 --token TOKEN]
set -e
cd "$(dirname "$0")"
if [ "$(id -u)" != 0 ]; then exec sudo "$0" "$@"; fi
./bond007 install "$@"
