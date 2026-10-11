#!/bin/sh
[ -n "$IPKG_INSTROOT" ] && exit 0
/etc/init.d/bond007 stop
/etc/init.d/bond007 disable
uci -q delete firewall.bond007
uci -q delete firewall.bond007_lan
uci -q commit firewall
/etc/init.d/firewall reload >/dev/null 2>&1
exit 0
