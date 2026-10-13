#!/bin/sh
# Route LAN clients through the bond: a firewall zone for the tunnel with
# masquerading and MSS clamping, and forwarding from lan. Works with fw3 and fw4.
[ -n "$IPKG_INSTROOT" ] && exit 0
uci -q batch <<UCI
delete firewall.netbond007
set firewall.netbond007=zone
set firewall.netbond007.name='netbond007'
add_list firewall.netbond007.device='net-bond-007'
set firewall.netbond007.input='REJECT'
set firewall.netbond007.output='ACCEPT'
set firewall.netbond007.forward='REJECT'
set firewall.netbond007.masq='1'
set firewall.netbond007.mtu_fix='1'
delete firewall.netbond007_lan
set firewall.netbond007_lan=forwarding
set firewall.netbond007_lan.src='lan'
set firewall.netbond007_lan.dest='netbond007'
commit firewall
UCI
/etc/init.d/firewall reload >/dev/null 2>&1
/etc/init.d/net-bond-007 enable
/etc/init.d/net-bond-007 restart
LAN=$(uci -q get network.lan.ipaddr | cut -d/ -f1)
echo "Net-Bond-007 installed. Dashboard: http://${LAN:-<router-ip>}:7007  (set a password in Settings > Dashboard)"
exit 0
