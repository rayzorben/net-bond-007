#!/bin/bash
# Double-click to install Net-Bond-007 as a background service (asks for your password).
cd "$(dirname "$0")"
xattr -d com.apple.quarantine ./net-bond-007 2>/dev/null
echo "Installing Net-Bond-007 (administrator password required)..."
sudo ./net-bond-007 install && echo "Done. Dashboard: http://127.0.0.1:7007"
read -n 1 -s -r -p "Press any key to close"
