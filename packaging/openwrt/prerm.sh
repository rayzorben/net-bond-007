#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
/etc/init.d/net-bond-007 stop
/etc/init.d/net-bond-007 disable
uci -q delete firewall.netbond007
uci -q delete firewall.netbond007_lan
uci -q commit firewall
/etc/init.d/firewall reload >/dev/null 2>&1
exit 0
