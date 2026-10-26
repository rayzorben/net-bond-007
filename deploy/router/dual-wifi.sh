#!/bin/sh
# Replace GL's single-uplink repeater with two Wi-Fi uplinks, one per radio:
#   sta1 on radio1 (2.4 GHz) -> network wwan
#   sta0 on radio0 (5 GHz)   -> network wwan5
# Each radio keeps serving the LAN SSIDs; its AP follows the uplink's channel.
#
# wifi-watchdog/ keeps each band's LAN SSID up when its uplink cannot
# connect (see the comment in wifi-uplink-watchdog); it is installed and
# started here and removed again on revert.
#
# Dead man's switch: after applying, the script waits DEADMAN seconds for
# /tmp/dualwifi/ok. If it never appears (lost SSH, LAN Wi-Fi gone, ...) every
# touched config is restored and the GL repeater is started again.
#
# Run detached on the router (SSH children die with the session):
#   scp -O deploy/router/dual-wifi.sh deploy/router/wifi-watchdog/* root@192.168.8.1:/tmp/
#   ssh root@192.168.8.1 'SSID="Ponderosa Guest" BSSID24=.. BSSID5=.. \
#       start-stop-daemon -S -b -x /tmp/dual-wifi.sh'
#   ssh root@192.168.8.1 'touch /tmp/dualwifi/ok'      # keep it
#   ssh root@192.168.8.1 '/tmp/dual-wifi.sh revert'     # undo by hand
set -u

B=/tmp/dualwifi
DEADMAN=${DEADMAN:-300}
SSID=${SSID:-}
KEY=${KEY:-}
BSSID24=${BSSID24:-}
BSSID5=${BSSID5:-}
CONFIGS="wireless network firewall repeater"

log() { logger -t dualwifi "$*"; echo "$(date '+%F %T') $*" >> $B/log; }

reload_all() {
	/etc/init.d/network reload
	sleep 3
	wifi reload
	/etc/init.d/firewall reload >/dev/null 2>&1
}

WD=wifi-uplink-watchdog

revert() {
	log "reverting to saved configs"
	if [ -x /etc/init.d/$WD ]; then
		/etc/init.d/$WD stop
		/etc/init.d/$WD disable
	fi
	rm -f /usr/sbin/$WD /etc/init.d/$WD
	for c in $CONFIGS; do cp "$B/$c" "/etc/config/$c"; done
	reload_all
	/etc/init.d/repeater enable
	/etc/init.d/repeater start
	log "reverted; GL repeater running again"
}

if [ "${1:-}" = revert ]; then
	revert
	exit 0
fi

mkdir -p $B
rm -f $B/ok
if [ -z "$SSID" ] || [ -z "$BSSID24" ] || [ -z "$BSSID5" ]; then
	log "SSID, BSSID24 and BSSID5 are required"
	exit 1
fi
if [ ! -f /tmp/$WD ] || [ ! -f /tmp/$WD.init ]; then
	log "/tmp/$WD and /tmp/$WD.init are required"
	exit 1
fi
for c in $CONFIGS; do cp "/etc/config/$c" "$B/$c"; done
log "saved configs to $B; applying ssid='$SSID' 2.4=$BSSID24 5=$BSSID5 deadman=${DEADMAN}s"

/etc/init.d/repeater stop
/etc/init.d/repeater disable
uci set repeater.@main[0].disabled=1
# GL leaves its own sta interfaces behind; netifd needs the names.
iw dev sta0 del 2>/dev/null
iw dev sta1 del 2>/dev/null

enc=none
[ -n "$KEY" ] && enc=psk2

set_sta() { # section radio ifname network bssid
	uci -q delete wireless.$1
	uci set wireless.$1=wifi-iface
	uci set wireless.$1.mode=sta
	uci set wireless.$1.device=$2
	uci set wireless.$1.ifname=$3
	uci set wireless.$1.network=$4
	uci set wireless.$1.ssid="$SSID"
	uci set wireless.$1.bssid=$5
	uci set wireless.$1.encryption=$enc
	[ -n "$KEY" ] && uci set wireless.$1.key="$KEY"
	uci set wireless.$1.disabled=0
}
mac24=$(uci -q get wireless.sta.macaddr)
set_sta sta radio1 sta1 wwan "$BSSID24"
[ -n "$mac24" ] && uci set wireless.sta.macaddr="$mac24"
set_sta sta5 radio0 sta0 wwan5 "$BSSID5"
# The 5 GHz AP must be able to follow the uplink onto any channel.
uci -q delete wireless.radio0.channels

uci -q delete network.wwan5
uci set network.wwan5=interface
uci set network.wwan5.proto=dhcp
uci set network.wwan5.metric=4
uci set network.wwan5.hostname='*'
uci set network.wwan5.classlessroute=0

wan=$(uci show firewall | sed -n "s/^firewall\.\(@zone\[[0-9]*\]\)\.name='wan'$/\1/p" | head -1)
uci -q del_list firewall.$wan.network=wwan5
uci add_list firewall.$wan.network=wwan5

uci commit
reload_all

cp /tmp/$WD /usr/sbin/$WD
cp /tmp/$WD.init /etc/init.d/$WD
chmod +x /usr/sbin/$WD /etc/init.d/$WD
/etc/init.d/$WD enable
/etc/init.d/$WD start
log "applied; watchdog running; waiting ${DEADMAN}s for $B/ok"

i=0
while [ $i -lt "$DEADMAN" ]; do
	if [ -f $B/ok ]; then
		log "confirmed; keeping dual uplinks"
		exit 0
	fi
	sleep 5
	i=$((i + 5))
done
log "no confirmation within ${DEADMAN}s"
revert
