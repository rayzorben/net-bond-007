#!/usr/bin/env bash
# Net-Bond-007 lab: a client and a server in two Linux network namespaces joined
# by three emulated links with different latency, bandwidth and loss. Your
# host's networking is untouched. Requires root (sudo) and iproute2 with netem.
#
#   sudo scripts/netns-lab.sh up       # build topology, start server + client
#   sudo scripts/netns-lab.sh test     # download through the bond
#   sudo scripts/netns-lab.sh cut eth1 # unplug a link (try mid-download)
#   sudo scripts/netns-lab.sh mend eth1
#   sudo scripts/netns-lab.sh down
#
# Dashboards (from the host): client http://10.250.0.2:7007  server http://10.250.1.2:8080
set -euo pipefail
cd "$(dirname "$0")/.."
BIN=${BIN:-dist/local}
CLI=b7cli SRV=b7srv
LAB=/tmp/net-bond-007-lab
# name  subnet  one-way-delay  rate  loss  [queue-limit-packets] [jitter]
# PROFILE=lab (default): three clean-ish links with deep buffers.
# PROFILE=starlink: what the GL Slate AX actually sees in the field: Starlink
#   (~76ms RTT, jitter, ~40 Mbit with a SHALLOW buffer so bursts drop) plus a
#   terrible campground Wi-Fi.
PROFILE=${PROFILE:-lab}
case "$PROFILE" in
  starlink) LINKS=("eth1 1 38ms 40mbit 0.2% 150 6ms" "wlan1 2 25ms 1200kbit 7% 100 10ms") ;; # campground Wi-Fi as measured: ~1.2 Mbit, ~13% round-trip loss
  field)    LINKS=("eth1 1 38ms 40mbit 0.2% 150 6ms" "wlan1 2 25ms 1200kbit 7% 100 10ms" "usb1 3 55ms 30mbit 0% 300 8ms") ;; # + iPhone USB tether (mark usb1 metered)
  *)        LINKS=("eth1 1 13ms 120mbit 0% 2000 0ms" "wlan1 2 23ms 80mbit 0.5% 2000 0ms" "usb1 3 50ms 30mbit 1% 2000 0ms") ;;
esac

ns() { ip netns exec "$@"; }

up() {
  down >/dev/null 2>&1 || true
  mkdir -p $LAB
  ip netns add $CLI; ip netns add $SRV
  ns $CLI ip link set lo up; ns $SRV ip link set lo up

  # Server: its public address and a pretend internet host.
  ns $SRV ip link add srv0 type dummy; ns $SRV ip addr add 10.200.0.1/32 dev srv0; ns $SRV ip link set srv0 up
  ns $SRV ip link add inet0 type dummy; ns $SRV ip addr add 198.51.100.10/32 dev inet0; ns $SRV ip link set inet0 up

  for spec in "${LINKS[@]}"; do
    read -r name n delay rate loss qlim jit <<<"$spec"
    ip link add "$name" netns $CLI type veth peer name "s$n" netns $SRV
    ns $CLI ip addr add "10.201.$n.2/24" dev "$name"; ns $CLI ip link set "$name" up
    ns $SRV ip addr add "10.201.$n.1/24" dev "s$n"; ns $SRV ip link set "s$n" up
    ns $CLI ip route add default via "10.201.$n.1" dev "$name" metric $((n*10))
    ns $CLI tc qdisc add dev "$name" root netem delay "$delay" "$jit" rate "$rate" loss "$loss" limit 2000
    ns $SRV tc qdisc add dev "s$n" root netem delay "$delay" "$jit" rate "$rate" loss "$loss" limit "$qlim"
  done

  # Management links so the dashboards are reachable from the host.
  ip link add b7m-cli type veth peer name mgmt0 netns $CLI
  ip addr add 10.250.0.1/30 dev b7m-cli; ip link set b7m-cli up
  ns $CLI ip addr add 10.250.0.2/30 dev mgmt0; ns $CLI ip link set mgmt0 up
  ip link add b7m-srv type veth peer name mgmt0 netns $SRV
  ip addr add 10.250.1.1/30 dev b7m-srv; ip link set b7m-srv up
  ns $SRV ip addr add 10.250.1.2/30 dev mgmt0; ns $SRV ip link set mgmt0 up

  # Big CDNs pace their TCP (BBR); emulate that for the "internet" host.
  if [ "$PROFILE" = starlink ]; then
    modprobe tcp_bbr 2>/dev/null || true
    ns $SRV sysctl -qw net.ipv4.tcp_congestion_control=bbr 2>/dev/null || true
  fi

  # Test payload served by the pretend internet host.
  mkdir -p $LAB/www && head -c 50M /dev/urandom > $LAB/www/50M.bin
  sha256sum $LAB/www/50M.bin | cut -d' ' -f1 > $LAB/50M.sha
  ns $SRV bash -c "cd $LAB/www && exec python3 -m http.server 80 --bind 198.51.100.10" >$LAB/www.log 2>&1 &

  cat > $LAB/server.json <<EOF
{"name":"lab-server","listen":":7007","token":"lab-token","ui":{"listen":"10.250.1.2:8080","refreshMs":500}}
EOF
  cat > $LAB/client.json <<EOF
{"name":"lab-client","server":{"address":"10.200.0.1:7007","token":"lab-token","enabled":true},
 "tunnel":{"interfaceName":"net-bond-007","mtu":1380,"fullTunnel":true},
 "interfaces":{"autoAdd":true,"scanMs":2000,"exclude":"lo*,mgmt*,net-bond-007*"},
 "ui":{"listen":"10.250.0.2:7007","refreshMs":500}}
EOF
  ns $SRV "$BIN/net-bond-007-server" --config $LAB/server.json >$LAB/server.log 2>&1 &
  sleep 0.5
  ns $CLI "$BIN/net-bond-007" run --config $LAB/client.json >$LAB/client.log 2>&1 &
  echo "waiting for the bond..."
  for _ in $(seq 1 40); do
    ns $CLI ip -4 addr show dev net-bond-007 2>/dev/null | grep -q inet && break
    sleep 0.25
  done
  sleep 1
  status
  echo
  echo "client dashboard: http://10.250.0.2:7007   server dashboard: http://10.250.1.2:8080"
}

status() {
  echo "--- client routes"; ns $CLI ip route
  echo "--- client links";  curl -s http://10.250.0.2:7007/api/state | python3 -c "$STATUS_PY"
}

STATUS_PY='
import json, sys
s = json.load(sys.stdin)
print("server connected:", s["server"]["connected"], " tunnel:", s["tunnel"].get("ip"), s["tunnel"].get("error", ""))
for l in s["links"]:
    print("  %-6s %-9s rtt=%6.1fms loss=%4.1f%% wins=%-7d dups=%-7d rx=%.1fMbit/s" % (
        l["name"], l["state"], l["rttMs"], l["lossPct"], l["wins"], l["dups"], l["rxBps"] / 1e6))
'

test_dl() {
  local want; want=$(cat $LAB/50M.sha)
  echo "downloading 50MB through the bond..."
  ns $CLI curl -s -o $LAB/got.bin -w 'http=%{http_code} time=%{time_total}s speed=%{speed_download}B/s\n' http://198.51.100.10/50M.bin
  local got; got=$(sha256sum $LAB/got.bin | cut -d' ' -f1)
  [[ $got == "$want" ]] && echo "integrity OK" || { echo "INTEGRITY FAILED"; exit 1; }
  ns $CLI ping -c 3 -W 2 198.51.100.10 || true
  status
}

# Baseline without the bond: same download straight over one link.
direct() {
  local ifc=${1:-eth1}
  ns $CLI curl -s --interface "$ifc" -o /dev/null -m 120 -w "direct via $ifc: %{time_total}s %{speed_download} B/s\n" http://198.51.100.10/50M.bin
}

cut_link()  { ns $CLI ip link set "$1" down; echo "$1 unplugged"; }
mend_link() { ns $CLI ip link set "$1" up; ns $CLI ip route add default via "10.201.$(link_n "$1").1" dev "$1" metric 50 2>/dev/null || true; echo "$1 restored"; }
link_n() { for spec in "${LINKS[@]}"; do read -r name n _ <<<"$spec"; [[ $name == "$1" ]] && echo "$n"; done; }

down() {
  pkill -f "net-bond-007-server --config $LAB" 2>/dev/null || true
  pkill -f "net-bond-007 run --config $LAB" 2>/dev/null || true
  pkill -f "http.server 80 --bind 198.51.100.10" 2>/dev/null || true
  ip link del b7m-cli 2>/dev/null || true
  ip link del b7m-srv 2>/dev/null || true
  ip netns del $CLI 2>/dev/null || true
  ip netns del $SRV 2>/dev/null || true
  echo "lab removed"
}

case "${1:-}" in
  up) up ;; down) down ;; status) status ;; test) test_dl ;;
  cut) cut_link "$2" ;; mend) mend_link "$2" ;; direct) direct "${2:-eth1}" ;;
  logs) tail -n 50 $LAB/client.log $LAB/server.log ;;
  *) sed -n '2,13p' "$0"; exit 2 ;;
esac
