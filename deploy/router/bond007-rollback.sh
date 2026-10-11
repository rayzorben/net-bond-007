#!/bin/sh
# Bond007 rollback for OpenWrt / GL.iNet. Runs ON THE ROUTER.
#
#   /root/bond007-rollback.sh          remove Bond007, undo its firewall/routing, re-enable multi-WAN
#   /root/bond007-rollback.sh --full   additionally restore the complete pre-install config
#                                      backup (/root/pre-bond007.tar.gz) and reboot
#
# Also used by the dead-man switch: if the install isn't confirmed in time,
# this runs automatically.
LOG=/root/bond007-rollback.log
exec >>"$LOG" 2>&1
echo "=== rollback $(date) args: $*"

/etc/init.d/bond007 stop 2>/dev/null
/etc/init.d/bond007 disable 2>/dev/null
pkill -f "/usr/bin/bond007 run" 2>/dev/null
opkg remove bond007 2>/dev/null

# Undo anything the package or the client may have left behind.
uci -q delete firewall.bond007
uci -q delete firewall.bond007_lan
uci commit firewall
nft delete table inet bond007 2>/dev/null
ip route del 0.0.0.0/1 dev bond007 2>/dev/null
ip route del 128.0.0.0/1 dev bond007 2>/dev/null
ip link del bond007 2>/dev/null

# Restore GL multi-WAN to its pre-install state.
if [ -f /root/bond007-pre-kmwan ]; then
  uci set kmwan.global.enable="$(cat /root/bond007-pre-kmwan)"
  uci commit kmwan
  /etc/init.d/kmwan restart 2>/dev/null
fi
/etc/init.d/firewall reload 2>/dev/null
rm -f /tmp/bond007-deadman.pid

if [ "$1" = "--full" ] && [ -f /root/pre-bond007.tar.gz ]; then
  echo "restoring full config backup and rebooting"
  sysupgrade -r /root/pre-bond007.tar.gz && sync && reboot
fi
echo "rollback complete"
