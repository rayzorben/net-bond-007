#!/usr/bin/env bash
# Run from your laptop to roll the router back:
#   deploy/router/rollback.sh            # remove Net-Bond-007, re-enable multi-WAN
#   deploy/router/rollback.sh --full     # also restore the full config backup and reboot
#
# If you can't reach the router over the network, plug into a LAN port (the LAN
# side is never routed through the bond) or open http://192.168.8.1 and use
# System > Backup/Restore with deploy/router/backups/<latest>/sysupgrade-config.tar.gz
set -euo pipefail
ROUTER=${ROUTER:-root@192.168.8.1}
ssh -o ConnectTimeout=8 -o LogLevel=ERROR "$ROUTER" "sh /root/net-bond-007-rollback.sh $*; tail -n 20 /root/net-bond-007-rollback.log"
