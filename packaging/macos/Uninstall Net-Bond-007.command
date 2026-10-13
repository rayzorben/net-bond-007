#!/bin/bash
cd "$(dirname "$0")"
sudo ./net-bond-007 uninstall
read -n 1 -s -r -p "Press any key to close"
