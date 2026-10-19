#!/bin/sh
# Net-Bond-007 rollback for OpenWrt / GL.iNet. Runs ON THE ROUTER.
#
#   /root/net-bond-007-rollback.sh          remove Net-Bond-007, undo its firewall/routing, re-enable multi-WAN
#   /root/net-bond-007-rollback.sh --full   additionally restore the complete pre-install config
#                                      backup (/root/pre-net-bond-007.tar.gz) and reboot
#
# Also used by the dead-man switch: if the install isn't confirmed in time,
# this runs automatically.
LOG=/root/net-bond-007-rollback.log
exec >>"$LOG" 2>&1
echo "=== rollback $(date) args: $*"

/etc/init.d/net-bond-007 stop 2>/dev/null
/etc/init.d/net-bond-007 disable 2>/dev/null
pkill -f "/usr/bin/net-bond-007 run" 2>/dev/null
opkg remove net-bond-007 2>/dev/null

# Undo anything the package or the client may have left behind.
uci -q delete firewall.netbond007
uci -q delete firewall.netbond007_lan
uci commit firewall
nft delete table inet net_bond_007 2>/dev/null
ip route del 0.0.0.0/1 dev net-bond-007 2>/dev/null
ip route del 128.0.0.0/1 dev net-bond-007 2>/dev/null
ip link del net-bond-007 2>/dev/null
# Connection pinning (0.7.2+) and the router-DNS-via-tunnel rules.
nft delete table ip net_bond_007_pin 2>/dev/null
while ip rule del pref 5899 2>/dev/null; do :; done
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  while ip rule del pref $((5900 + i)) 2>/dev/null; do :; done
  ip route flush table $((7100 + i)) 2>/dev/null
done
for d in $(ip rule | awk '/lookup 7007/ {print $5}'); do
  while ip rule del to "$d" lookup 7007 2>/dev/null; do :; done
done
ip route flush table 7007 2>/dev/null
# Port forwarding (0.8.6+).
nft delete table ip net_bond_007_ports 2>/dev/null
while ip rule del pref 5898 2>/dev/null; do :; done
ip route flush table 7090 2>/dev/null
rm -f /usr/share/nftables.d/chain-pre/*/50-net-bond-007-ports.nft
nft -a list table inet fw4 2>/dev/null | awk '/^\tchain /{c=$2} /net-bond-007 port forwards/{print c, $NF}' | while read -r c h; do nft delete rule inet fw4 "$c" handle "$h"; done

# Restore GL multi-WAN to its pre-install state.
if [ -f /root/net-bond-007-pre-kmwan ]; then
  uci set kmwan.global.enable="$(cat /root/net-bond-007-pre-kmwan)"
  uci commit kmwan
  /etc/init.d/kmwan restart 2>/dev/null
fi
/etc/init.d/firewall reload 2>/dev/null
rm -f /tmp/net-bond-007-deadman.pid

if [ "$1" = "--full" ] && [ -f /root/pre-net-bond-007.tar.gz ]; then
  echo "restoring full config backup and rebooting"
  sysupgrade -r /root/pre-net-bond-007.tar.gz && sync && reboot
fi
echo "rollback complete"
