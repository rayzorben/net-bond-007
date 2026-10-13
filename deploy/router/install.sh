#!/usr/bin/env bash
# Install Net-Bond-007 on a GL.iNet / OpenWrt router with layered safety:
#   1. Requires a config backup in deploy/router/backups/ (made beforehand).
#   2. Copies the backup and a rollback script onto the router (/root).
#   3. Arms a dead-man switch: unless you run `deploy/router/install.sh confirm`
#      within DEADMAN seconds, the router rolls itself back automatically.
#   4. Starts Net-Bond-007 but does NOT enable it at boot until confirmed, so a
#      simple power-cycle also restores the old behaviour.
#
#   deploy/router/install.sh            # install + arm dead-man switch (10 min)
#   deploy/router/install.sh confirm    # keep it: disarm switch, enable at boot
#   deploy/router/rollback.sh [--full]  # undo
set -euo pipefail
cd "$(dirname "$0")/../.."
ROUTER=${ROUTER:-root@192.168.8.1}
DEADMAN=${DEADMAN:-600}
IPK=${IPK:-$(ls dist/release/net-bond-007_*_aarch64_cortex-a53_neon-vfpv4.ipk | tail -1)}
R() { ssh -o BatchMode=yes -o ConnectTimeout=8 -o LogLevel=ERROR "$ROUTER" "$@"; }
say() { printf '\033[1;35m==> %s\033[0m\n' "$*"; }

if [ "${1:-}" = confirm ]; then
  # The flag alone already disarms the switch; also kill the timer and its
  # sleep child so nothing is left running.
  R 'touch /tmp/net-bond-007-confirmed; [ -f /tmp/net-bond-007-deadman.pid ] && kill $(cat /tmp/net-bond-007-deadman.pid) 2>/dev/null; for p in $(ps w | grep "[s]leep '"$DEADMAN"'" | awk "{print \$1}"); do kill $p; done; rm -f /tmp/net-bond-007-deadman.pid; /etc/init.d/net-bond-007 enable; echo "confirmed: Net-Bond-007 stays and starts at boot"'
  exit 0
fi

BACKUP=$(ls -d deploy/router/backups/slate-ax-* 2>/dev/null | tail -1)
[ -n "$BACKUP" ] && [ -s "$BACKUP/sysupgrade-config.tar.gz" ] || { echo "No backup found in deploy/router/backups/. Back up first."; exit 1; }
ENVFILE=deploy/router/.router.env
[ -f "$ENVFILE" ] && . "$ENVFILE"
ROUTER_UI_PASSWORD=${ROUTER_UI_PASSWORD:-$(openssl rand -base64 12 | tr -d '/+=')}
SERVER=${SERVER:?set SERVER=<server-host>:7007 in $ENVFILE}   # your Net-Bond-007 server (UDP 7007)
NET_BOND_007_TOKEN=${NET_BOND_007_TOKEN:?set NET_BOND_007_TOKEN in $ENVFILE}   # server access token
RELAY_TOKEN=${RELAY_TOKEN:-$(tr -dc 'a-km-np-z2-9' </dev/urandom | head -c 8)}   # pairing token for the phone relay app
(umask 077; printf 'ROUTER_UI_PASSWORD=%s\nSERVER=%s\nNET_BOND_007_TOKEN=%s\nRELAY_TOKEN=%s\n' "$ROUTER_UI_PASSWORD" "$SERVER" "$NET_BOND_007_TOKEN" "$RELAY_TOKEN" > "$ENVFILE")
# Multi-WAN state before we touched anything (from the backup).
PRE_KMWAN=$(awk '/^package kmwan/{p=1} p&&/option enable/{gsub(/\x27/,"",$3); print $3; exit}' "$BACKUP/uci-export.txt")

say "Uploading backup, rollback script and package ($IPK)"
R "cat > /root/pre-net-bond-007.tar.gz" < "$BACKUP/sysupgrade-config.tar.gz"
R "cat > /root/net-bond-007-rollback.sh && chmod +x /root/net-bond-007-rollback.sh" < deploy/router/net-bond-007-rollback.sh
R "echo ${PRE_KMWAN:-1} > /root/net-bond-007-pre-kmwan"
R "cat > /tmp/net-bond-007.ipk" < "$IPK"

say "Arming dead-man switch: automatic rollback in $DEADMAN s unless confirmed"
R "rm -f /tmp/net-bond-007-confirmed; sh -c 'trap \"\" HUP; sleep $DEADMAN; [ -f /tmp/net-bond-007-confirmed ] || sh /root/net-bond-007-rollback.sh' </dev/null >/dev/null 2>&1 & echo \$! > /tmp/net-bond-007-deadman.pid"

say "Installing package"
R "opkg install /tmp/net-bond-007.ipk 2>&1 | tail -3"
R "/etc/init.d/net-bond-007 stop; /etc/init.d/net-bond-007 disable"   # not at boot until confirmed

say "Configuring client"
R "cat > /etc/net-bond-007/client.json && chmod 600 /etc/net-bond-007/client.json" <<JSON
{
  "name": "slate-ax",
  "server": { "address": "$SERVER", "token": "$NET_BOND_007_TOKEN", "enabled": true },
  "tunnel": { "fullTunnel": true, "routerMode": true, "dnsOverride": false },
  "interfaces": { "autoAdd": true, "exclude": "lo*,br-*,lan*,docker*,veth*,tailscale*,wg*,tun*,tap*,net-bond-007*,ifb*,dummy*,sit*,ip6tnl*,gre*,phy*,mon*,wlan*,ra*,rai*,ath*,wifi*" },
  "relay": { "enabled": true, "token": "$RELAY_TOKEN", "listen": "192.168.8.1:7008" },
  "ui": { "listen": "0.0.0.0:7007", "password": "$ROUTER_UI_PASSWORD" }
}
JSON
R "/etc/init.d/net-bond-007 start"

say "Waiting for the bond"
for _ in $(seq 1 30); do
  R "ip -4 addr show dev net-bond-007 2>/dev/null | grep -q inet" && break
  sleep 1
done
R 'echo "--- links"; logread -e net-bond-007 | grep -E "link added|connected to server|adapter up|cannot|error" | tail -8; echo "--- routes"; ip route | grep -E "net-bond-007|default"; echo "--- memory"; ps w | grep "[b]ond007 run" | awk "{print \"net-bond-007 RSS:\", \$3}"'
cat <<OUT

  Router dashboard: http://192.168.8.1:7007   (password in $ENVFILE)
  Test from a LAN device:  curl https://ifconfig.me   (should be the home IP)

  Happy?   deploy/router/install.sh confirm      (otherwise it rolls back in $((DEADMAN/60)) min)
  Undo:    deploy/router/rollback.sh [--full]
OUT
