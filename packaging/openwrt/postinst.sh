#!/bin/sh
# Route LAN clients through the bond: a firewall zone for the tunnel with
# masquerading and MSS clamping, and forwarding from lan. Works with fw3 and fw4.
[ -n "$IPKG_INSTROOT" ] && exit 0
uci -q batch <<UCI
delete firewall.bond007
set firewall.bond007=zone
set firewall.bond007.name='bond007'
add_list firewall.bond007.device='bond007'
set firewall.bond007.input='REJECT'
set firewall.bond007.output='ACCEPT'
set firewall.bond007.forward='REJECT'
set firewall.bond007.masq='1'
set firewall.bond007.mtu_fix='1'
delete firewall.bond007_lan
set firewall.bond007_lan=forwarding
set firewall.bond007_lan.src='lan'
set firewall.bond007_lan.dest='bond007'
commit firewall
UCI
/etc/init.d/firewall reload >/dev/null 2>&1
/etc/init.d/bond007 enable
/etc/init.d/bond007 restart
LAN=$(uci -q get network.lan.ipaddr | cut -d/ -f1)
echo "Bond007 installed. Dashboard: http://${LAN:-<router-ip>}:7007  (set a password in Settings > Dashboard)"
exit 0
