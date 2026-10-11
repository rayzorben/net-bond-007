#!/bin/bash
# Double-click to install Bond007 as a background service (asks for your password).
cd "$(dirname "$0")"
xattr -d com.apple.quarantine ./bond007 2>/dev/null
echo "Installing Bond007 (administrator password required)..."
sudo ./bond007 install && echo "Done. Dashboard: http://127.0.0.1:7007"
read -n 1 -s -r -p "Press any key to close"
