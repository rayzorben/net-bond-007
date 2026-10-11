#!/bin/sh
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  systemctl enable --now bond007.service || true
fi
echo
echo "  Bond007 is running. Open the dashboard: http://127.0.0.1:7007"
echo
