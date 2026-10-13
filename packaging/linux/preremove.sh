#!/bin/sh
if command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now net-bond-007.service || true
fi
