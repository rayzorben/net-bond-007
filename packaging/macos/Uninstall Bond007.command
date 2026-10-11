#!/bin/bash
cd "$(dirname "$0")"
sudo ./bond007 uninstall
read -n 1 -s -r -p "Press any key to close"
