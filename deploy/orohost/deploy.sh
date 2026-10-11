#!/usr/bin/env bash
# Build the Bond007 server image, push it to Docker Hub, and roll it out on
# orohost (home server, Docker) through stay-go.
#
#   deploy/orohost/deploy.sh                 # version = short source hash
#   VERSION=0.2.0 deploy/orohost/deploy.sh
#   SKIP_PUSH=1 deploy/orohost/deploy.sh     # just re-apply on orohost
#
# The container itself is declared in stay-go, not here:
#   stay-go/config/hosts/orohost.yaml        compose: entry + secrets.bond007.*
#   stay-go/config/files/docker/bond007/     docker-compose.yml + .env
# Server config persists in /storage/apps/bond007/server.json on orohost.
#
# Needs `docker login -u rayzorben` on this machine (push) and on orohost (pull).
set -euo pipefail
cd "$(dirname "$0")/../.."

IMAGE=${IMAGE:-rayzorben/bond007-server}
HOST=${HOST:-rayben@orohost}
STAYGO=${STAYGO:-/storage/dropbox/Dropbox/PC/development/stay-go}
VERSION=${VERSION:-$(find Dockerfile go.mod go.sum cmd internal web -path web/node_modules -prune -o -path internal/webui/dist -prune -o -type f -print | LC_ALL=C sort | xargs sha256sum | sha256sum | cut -c1-12)}

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
if R "docker ps -a --format '{{.Names}}' | grep -qx bond007"; then
  T "cd $STAYGO && stay-go -c config --update=compose/bond007 --yes"
else
  T "cd $STAYGO && stay-go -c config --apply=compose/bond007 --yes"
fi

say "Waiting for the dashboard"
for _ in $(seq 1 30); do
  R "curl -sf -m 3 http://127.0.0.1:8077/api/info" >/dev/null && break
  sleep 2
done
INFO=$(R "curl -sf -m 3 http://127.0.0.1:8077/api/info") || { R "docker logs --tail 40 bond007"; exit 1; }
# stay-go exits 0 even when a node fails, so check what is actually running.
RUNNING=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' <<<"$INFO")
if [ -z "${SKIP_PUSH:-}" ] && [ "$RUNNING" != "$VERSION" ]; then
  echo "orohost is still running $RUNNING, not $VERSION (stay-go step failed?)"; exit 1
fi
R "docker logs --tail 5 bond007"
cat <<OUT

  Bond007 server is live on orohost ($RUNNING)
    Bond (UDP):  $(R "curl -s -m 5 -4 ifconfig.me" || echo "<home public IP>"):7007  (home router forwards UDP 7007 -> 192.168.1.50)
    Dashboard:   http://192.168.1.50:8077  or  http://orohost:8077 over Tailscale
    Secrets:     stay-go secrets.bond007.{token,ui_password} (copy in deploy/orohost/.bond007.env)
OUT
