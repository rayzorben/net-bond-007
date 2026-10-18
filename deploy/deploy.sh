#!/usr/bin/env bash
# Deploy the current tree to the Slate AX router and the OCI server.
#
# Every deploy is a clean install:
#   - all old versions are removed first (local build output, old router
#     binaries/packages/leftovers, every old server image tag),
#   - the new version is installed with bonding turned OFF on the router
#     (server.enabled=false). Turn it on from the dashboard when ready.
#
#   deploy/deploy.sh              # router + server
#   deploy/deploy.sh router       # router only
#   deploy/deploy.sh server       # server only
#   SKIP_TESTS=1 deploy/deploy.sh
set -euo pipefail
cd "$(dirname "$0")/.."
ROUTER=${ROUTER:-root@192.168.8.1}
SERVER_SSH=${SERVER_SSH:-ubuntu@150.136.117.176}
IMAGE=rayzorben/net-bond-007-server
OWRT_ARCH=aarch64_cortex-a53_neon-vfpv4
NFPM=${NFPM:-.cache/bin/nfpm}
VERSION=$(cat internal/version/VERSION)
WHAT=${1:-all}
SSHO=(-o BatchMode=yes -o ConnectTimeout=10 -o LogLevel=ERROR)
R() { ssh "${SSHO[@]}" "$ROUTER" "$@"; }
S() { ssh "${SSHO[@]}" "$SERVER_SSH" "$@"; }
say() { printf '\n\033[1;35m==> %s\033[0m\n' "$*"; }

say "Net-Bond-007 $VERSION: clean local build output"
rm -rf dist/release dist/bin
# Old version binaries kept as rollback copies are old versions too.
find deploy/router/backups -type f -name 'net-bond-007*' -exec sh -c 'head -c4 "$1" | grep -q ELF && rm -f "$1"' _ {} \; 2>/dev/null || true
find deploy/router/backups -mindepth 1 -type d -empty -delete 2>/dev/null || true
mkdir -p dist/release dist/bin

(cd web && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build >/dev/null)
[ -n "${SKIP_TESTS:-}" ] || { say "Tests"; go test -count=1 ./... >/dev/null; }

deploy_router() {
  say "Router: build $OWRT_ARCH package"
  local bin=dist/bin/linux-arm64/net-bond-007 cfg ipk
  mkdir -p "$(dirname "$bin")"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$bin" ./cmd/net-bond-007
  cfg=$(mktemp --suffix=.yaml)
  BIN=$bin NFPM_ARCH=$OWRT_ARCH VERSION=$VERSION python3 -c 'import os,sys; print(os.path.expandvars(open(sys.argv[1]).read()))' packaging/nfpm-openwrt.yaml > "$cfg"
  "$NFPM" package -f "$cfg" -p ipk -t dist/release/ >/dev/null
  rm -f "$cfg"
  ipk=dist/release/net-bond-007_${VERSION}_${OWRT_ARCH}.ipk

  say "Router: save settings, force bonding off"
  local keep; keep=$(mktemp -d)
  R 'cat /etc/net-bond-007/client.json' > "$keep/client.json"
  python3 - "$keep/client.json" <<'PY'
import json, sys
p = sys.argv[1]
c = json.load(open(p))
c.setdefault('server', {})['enabled'] = False
json.dump(c, open(p, 'w'), indent=2)
PY
  for f in usage.json speedtests.json; do R "cat /etc/net-bond-007/$f 2>/dev/null" > "$keep/$f" || true; done

  say "Router: remove every old version"
  R 'opkg remove net-bond-007 >/dev/null 2>&1 || /etc/init.d/net-bond-007 stop 2>/dev/null
     /etc/init.d/net-bond-007 disable 2>/dev/null
     rm -rf /usr/bin/net-bond-007* /etc/init.d/net-bond-007 /tmp/nb /tmp/net-bond-007* /tmp/install0* \
            /root/net-bond-007.* /root/net-bond-007-rollback.log /root/pre-0.7.0 /root/client.json.* \
            /etc/net-bond-007/*-opkg /etc/net-bond-007/client.json.*
     ip rule | grep -qE "lookup (7007|71[0-9][0-9])" && echo "note: leftover ip rules:" && ip rule | grep -E "lookup (7007|71[0-9][0-9])"
     true'

  say "Router: install $VERSION with bonding off"
  R 'mkdir -p /etc/net-bond-007'
  R 'cat > /etc/net-bond-007/client.json && chmod 600 /etc/net-bond-007/client.json' < "$keep/client.json"
  for f in usage.json speedtests.json; do [ -s "$keep/$f" ] && R "cat > /etc/net-bond-007/$f" < "$keep/$f"; done
  rm -rf "$keep"
  R 'cat > /tmp/net-bond-007.ipk' < "$ipk"
  R 'opkg install /tmp/net-bond-007.ipk 2>&1 | grep -v "^Configuring\|^Installing\|Dashboard:" ; rm -f /tmp/net-bond-007.ipk /etc/net-bond-007/*-opkg'

  say "Router: verify"
  local v=""
  for _ in $(seq 1 20); do
    v=$(R 'curl -s -m2 localhost:7007/api/info' | python3 -c 'import json,sys; print(json.load(sys.stdin)["info"]["version"])' 2>/dev/null) && [ -n "$v" ] && break
    sleep 1
  done
  R "opkg list-installed | grep net-bond-007; jsonfilter -i /etc/net-bond-007/client.json -e '@.server.enabled' | sed 's/^/bonding enabled: /'; ls /usr/bin | grep -c net-bond-007 | sed 's/^/binaries: /'; df -h /overlay | tail -1"
  [ "$v" = "$VERSION" ] || { echo "router reports '$v', expected $VERSION"; exit 1; }
  echo "router runs $v"
}

deploy_server() {
  say "Server: sync source and build $IMAGE:$VERSION"
  rsync -az --delete --exclude .git --exclude node_modules --exclude /dist --exclude .cache --exclude .claude \
    --exclude 'deploy/router/backups' --exclude '*.env' ./ "$SERVER_SSH":net-bond-007-src/ 2>&1 | grep -v '^\*\*' || true
  S "cd ~/net-bond-007-src && sudo docker build -q --build-arg VERSION=$VERSION -t $IMAGE:$VERSION -t $IMAGE:latest . >/dev/null"

  say "Server: restart on $VERSION, remove every old image"
  # Detached so a dropped SSH session (the PC may ride the bond) can't interrupt it.
  S "sudo setsid sh -c 'cd /opt/net-bond-007 && docker compose up -d --force-recreate >/tmp/nb-deploy.log 2>&1' </dev/null >/dev/null 2>&1 &"
  for _ in $(seq 1 30); do
    S "sudo docker inspect -f '{{.Config.Image}} {{.State.Status}}' net-bond-007 2>/dev/null" | grep -q running && break
    sleep 2
  done
  S "for t in \$(sudo docker images $IMAGE --format '{{.Tag}}'); do
       case \$t in $VERSION|latest) ;; *) sudo docker rmi -f $IMAGE:\$t >/dev/null ;; esac
     done
     sudo docker image prune -f >/dev/null
     sudo docker images $IMAGE --format '{{.Repository}}:{{.Tag}} {{.ID}}'
     curl -s -m3 localhost:8077/api/info; echo"
}

case $WHAT in
  router) deploy_router ;;
  server) deploy_server ;;
  all) deploy_router; deploy_server ;;
  *) echo "usage: $0 [router|server|all]"; exit 2 ;;
esac
say "Done: $VERSION deployed, bonding OFF. Turn it on from the router dashboard when ready."
