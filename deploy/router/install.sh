#!/usr/bin/env bash
# Install Bond007 on a GL.iNet / OpenWrt router with layered safety:
#   1. Requires a config backup in deploy/router/backups/ (made beforehand).
#   2. Copies the backup and a rollback script onto the router (/root).
#   3. Arms a dead-man switch: unless you run `deploy/router/install.sh confirm`
#      within DEADMAN seconds, the router rolls itself back automatically.
#   4. Starts Bond007 but does NOT enable it at boot until confirmed, so a
#      simple power-cycle also restores the old behaviour.
#
#   deploy/router/install.sh            # install + arm dead-man switch (10 min)
#   deploy/router/install.sh confirm    # keep it: disarm switch, enable at boot
#   deploy/router/rollback.sh [--full]  # undo
set -euo pipefail
cd "$(dirname "$0")/../.."
ROUTER=${ROUTER:-root@192.168.8.1}
DEADMAN=${DEADMAN:-600}
IPK=${IPK:-$(ls dist/release/bond007_*_aarch64_cortex-a53_neon-vfpv4.ipk | tail -1)}
R() { ssh -o BatchMode=yes -o ConnectTimeout=8 -o LogLevel=ERROR "$ROUTER" "$@"; }
say() { printf '\033[1;35m==> %s\033[0m\n' "$*"; }

if [ "${1:-}" = confirm ]; then
  # The flag alone already disarms the switch; also kill the timer and its
  # sleep child so nothing is left running.
  R 'touch /tmp/bond007-confirmed; [ -f /tmp/bond007-deadman.pid ] && kill $(cat /tmp/bond007-deadman.pid) 2>/dev/null; for p in $(ps w | grep "[s]leep '"$DEADMAN"'" | awk "{print \$1}"); do kill $p; done; rm -f /tmp/bond007-deadman.pid; /etc/init.d/bond007 enable; echo "confirmed: Bond007 stays and starts at boot"'
  exit 0
fi

BACKUP=$(ls -d deploy/router/backups/slate-ax-* 2>/dev/null | tail -1)
[ -n "$BACKUP" ] && [ -s "$BACKUP/sysupgrade-config.tar.gz" ] || { echo "No backup found in deploy/router/backups/. Back up first."; exit 1; }
. deploy/orohost/.bond007.env
ENVFILE=deploy/router/.router.env
[ -f "$ENVFILE" ] && . "$ENVFILE"
ROUTER_UI_PASSWORD=${ROUTER_UI_PASSWORD:-$(openssl rand -base64 12 | tr -d '/+=')}
(umask 077; echo "ROUTER_UI_PASSWORD=$ROUTER_UI_PASSWORD" > "$ENVFILE")
SERVER=${SERVER:?set SERVER in deploy/router/.router.env}   # orohost via the home router's UDP 7007 forward
# Multi-WAN state before we touched anything (from the backup).
PRE_KMWAN=$(awk '/^package kmwan/{p=1} p&&/option enable/{gsub(/\x27/,"",$3); print $3; exit}' "$BACKUP/uci-export.txt")

say "Uploading backup, rollback script and package ($IPK)"
R "cat > /root/pre-bond007.tar.gz" < "$BACKUP/sysupgrade-config.tar.gz"
R "cat > /root/bond007-rollback.sh && chmod +x /root/bond007-rollback.sh" < deploy/router/bond007-rollback.sh
R "echo ${PRE_KMWAN:-1} > /root/bond007-pre-kmwan"
R "cat > /tmp/bond007.ipk" < "$IPK"

say "Arming dead-man switch: automatic rollback in $DEADMAN s unless confirmed"
R "rm -f /tmp/bond007-confirmed; sh -c 'trap \"\" HUP; sleep $DEADMAN; [ -f /tmp/bond007-confirmed ] || sh /root/bond007-rollback.sh' </dev/null >/dev/null 2>&1 & echo \$! > /tmp/bond007-deadman.pid"

say "Installing package"
R "opkg install /tmp/bond007.ipk 2>&1 | tail -3"
R "/etc/init.d/bond007 stop; /etc/init.d/bond007 disable"   # not at boot until confirmed

say "Configuring client"
R "cat > /etc/bond007/client.json && chmod 600 /etc/bond007/client.json" <<JSON
{
  "name": "slate-ax",
  "server": { "address": "$SERVER", "token": "$BOND007_TOKEN", "enabled": true },
  "tunnel": { "fullTunnel": true, "routerMode": true, "dnsOverride": false },
  "interfaces": { "autoAdd": true, "exclude": "lo*,br-*,lan*,docker*,veth*,tailscale*,wg*,tun*,tap*,bond007*,ifb*,dummy*,sit*,ip6tnl*,gre*,phy*,mon*,wlan*,ra*,rai*,ath*,wifi*" },
  "ui": { "listen": "0.0.0.0:7007", "password": "$ROUTER_UI_PASSWORD" }
}
JSON
R "/etc/init.d/bond007 start"

say "Waiting for the bond"
for _ in $(seq 1 30); do
  R "ip -4 addr show dev bond007 2>/dev/null | grep -q inet" && break
  sleep 1
done
R 'echo "--- links"; logread -e bond007 | grep -E "link added|connected to server|adapter up|cannot|error" | tail -8; echo "--- routes"; ip route | grep -E "bond007|default"; echo "--- memory"; ps w | grep "[b]ond007 run" | awk "{print \"bond007 RSS:\", \$3}"'
cat <<OUT

  Router dashboard: http://192.168.8.1:7007   (password in $ENVFILE)
  Test from a LAN device:  curl https://ifconfig.me   (should be the home IP)

  Happy?   deploy/router/install.sh confirm      (otherwise it rolls back in $((DEADMAN/60)) min)
  Undo:    deploy/router/rollback.sh [--full]
OUT
