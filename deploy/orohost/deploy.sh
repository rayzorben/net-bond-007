#!/usr/bin/env bash
# Build the Net-Bond-007 server image, push it to Docker Hub, and roll it out on
# orohost (home server, Docker) through stay-go.
#
#   deploy/orohost/deploy.sh                 # version = short source hash
#   VERSION=0.2.0 deploy/orohost/deploy.sh
#   SKIP_PUSH=1 deploy/orohost/deploy.sh     # just re-apply on orohost
#
# The container itself is declared in stay-go, not here:
#   stay-go/config/hosts/orohost.yaml        compose: entry + secrets.net-bond-007.*
#   stay-go/config/files/docker/net-bond-007/     docker-compose.yml + .env
# Server config persists in /storage/apps/net-bond-007/server.json on orohost.
#
# Needs `docker login -u rayzorben` on this machine (push) and on orohost (pull).
set -euo pipefail
cd "$(dirname "$0")/../.."

IMAGE=${IMAGE:-rayzorben/net-bond-007-server}
HOST=${HOST:-rayben@orohost}
STAYGO=${STAYGO:-/storage/dropbox/Dropbox/PC/development/stay-go}
VERSION=${VERSION:-$(cat internal/version/VERSION)}

say() { printf '\033[1;35m==> %s\033[0m\n' "$*"; }
R() { ssh -o BatchMode=yes -o ConnectTimeout=8 "$HOST" bash -c "$(printf '%q' "$1")"; }
# stay-go prompts for the secrets password, so it needs a real terminal.
T() { ssh -t -o ConnectTimeout=8 "$HOST" bash -c "$(printf '%q' "$1")"; }

if [ -z "${SKIP_PUSH:-}" ]; then
  say "Building $IMAGE:$VERSION"
  docker build --build-arg VERSION="$VERSION" -t "$IMAGE:$VERSION" -t "$IMAGE:latest" .
  say "Pushing to Docker Hub"
  docker push "$IMAGE:$VERSION"
  docker push "$IMAGE:latest"
fi

# First run: apply creates the project. Later runs: update pulls :latest and
# recreates the container only if the image changed. --apply/--update touch
# only this project, not the rest of orohost's pending plan. You will be asked
# for the stay-go secrets password.
say "Rolling out on $HOST"
# One-time move from the pre-rename install: its container holds the ports,
# and its data (server config, history) moves to the new path.
LEGACY="bond""007"
if R "docker ps -a --format '{{.Names}}' | grep -qx $LEGACY"; then
  say "Retiring the pre-rename container and moving its data"
  R "docker rm -f $LEGACY >/dev/null"
  R "[ -d /storage/apps/$LEGACY ] && [ ! -e /storage/apps/net-bond-007 ] && docker run --rm -v /storage/apps:/a alpine mv /a/$LEGACY /a/net-bond-007 || true"
fi
if R "docker ps -a --format '{{.Names}}' | grep -qx net-bond-007"; then
  T "cd $STAYGO && stay-go -c config --update=compose/net-bond-007 --yes"
else
  T "cd $STAYGO && stay-go -c config --apply=compose/net-bond-007 --yes"
fi

say "Waiting for the dashboard"
for _ in $(seq 1 30); do
  R "curl -sf -m 3 http://127.0.0.1:8077/api/info" >/dev/null && break
  sleep 2
done
INFO=$(R "curl -sf -m 3 http://127.0.0.1:8077/api/info") || { R "docker logs --tail 40 net-bond-007"; exit 1; }
# stay-go exits 0 even when a node fails, so check what is actually running.
RUNNING=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' <<<"$INFO")
if [ -z "${SKIP_PUSH:-}" ] && [ "$RUNNING" != "$VERSION" ]; then
  echo "orohost is still running $RUNNING, not $VERSION (stay-go step failed?)"; exit 1
fi
R "docker logs --tail 5 net-bond-007"
cat <<OUT

  Net-Bond-007 server is live on orohost ($RUNNING)
    Bond (UDP):  $(R "curl -s -m 5 -4 ifconfig.me" || echo "<home public IP>"):7007  (home router forwards UDP 7007 -> 192.168.1.50)
    Dashboard:   http://192.168.1.50:8077  or  http://orohost:8077 over Tailscale
    Secrets:     stay-go secrets.net-bond-007.{token,ui_password} (copy in deploy/orohost/.net-bond-007.env)
OUT
