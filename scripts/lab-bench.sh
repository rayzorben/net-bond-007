#!/usr/bin/env bash
# Benchmark bonding modes in the netns lab (run scripts/netns-lab.sh up first).
# usage: sudo scripts/lab-bench.sh [modes...] [-- strategy]
set -euo pipefail
API=http://10.250.0.2:7007/api/config
LAB=/tmp/bond007-lab
modes=("${@:-smart speed mirror}")
set_cfg() { curl -s $API | python3 -c "import json,sys; c=json.load(sys.stdin); c['bonding']['$1']='$2'; print(json.dumps(c))" | curl -sf -o /dev/null -X PUT --data-binary @- $API; }
want=$(cut -c1-12 $LAB/50M.sha)
for mode in ${modes[@]}; do
  set_cfg mode "$mode"; sleep 2
  for i in 1 2 3; do
    out=$(ip netns exec b7cli curl -s -m 120 -o $LAB/got.bin -w '%{time_total} %{speed_download}' http://198.51.100.10/50M.bin || echo "fail 0")
    read -r t sp <<<"$out"
    got=$(sha256sum $LAB/got.bin | cut -c1-12)
    printf '%-7s run%d  %6.2fs  %6.1f Mbit/s  %s\n' "$mode" "$i" "$t" "$(python3 -c "print($sp*8/1e6)")" "$([[ $got == "$want" ]] && echo intact || echo CORRUPT)"
  done
done
