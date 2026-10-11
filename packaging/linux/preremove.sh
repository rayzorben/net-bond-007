#!/bin/sh
if command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now bond007.service || true
fi
