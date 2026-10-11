#!/usr/bin/env bash
# Build every Bond007 deliverable into dist/release:
#   - Windows   bond007-<v>-windows-{amd64,arm64}.zip   (self-installing exe)
#   - macOS     bond007-<v>-macos-{arm64,amd64}.tar.gz  (double-click installer)
#   - Linux     .deb / .rpm / Arch packages + .tar.gz   (amd64, arm64)
#   - OpenWrt   .ipk for GL.iNet Slate AX (aarch64_cortex-a53) and others
#   - Server    Docker image bond007-server:<v> (+ linux binaries)
# Requires Go and Node. Skips Docker if it is not available. SKIP_TESTS=1 to skip tests.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=${VERSION:-0.1.0}
OUT=dist/release
NFPM=${NFPM:-.cache/bin/nfpm}
LDFLAGS="-s -w -X main.version=$VERSION"
export CGO_ENABLED=0

# nfpm doesn't expand env vars in file paths: render the config per target.
pkg() { # yaml format bin arch
  local cfg; cfg=$(mktemp --suffix=.yaml)
  BIN=$3 NFPM_ARCH=$4 VERSION=$VERSION python3 -c 'import os,sys; print(os.path.expandvars(open(sys.argv[1]).read()))' "$1" > "$cfg"
  "$NFPM" package -f "$cfg" -p "$2" -t "$OUT/" >/dev/null
  rm -f "$cfg"
}

step() { printf '\n\033[1;35m==> %s\033[0m\n' "$*"; }

step "Dashboard (Svelte)"
(cd web && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build)

if [ -z "${SKIP_TESTS:-}" ]; then
  step "Unit + end-to-end tests"
  go test -count=1 ./...
fi

[ -x "$NFPM" ] || { step "Fetching nfpm"; GOBIN=$PWD/.cache/bin go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest; }

rm -rf "$OUT" && mkdir -p "$OUT" dist/bin
build() { # os arch [goarm|gomips] -> path
  local os=$1 arch=$2 extra=${3:-} out="dist/bin/$1-$2${3:+-$3}/bond007"
  [ "$os" = windows ] && out="$out.exe"
  mkdir -p "$(dirname "$out")"
  GOOS=$os GOARCH=$arch GOARM=${extra#v} GOMIPS=softfloat go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/bond007
  echo "$out"
}

step "Windows"
for a in amd64 arm64; do
  b=$(build windows $a); d=$(mktemp -d)
  cp "$b" packaging/windows/README.txt "$d/"
  (cd "$d" && python3 -c "import shutil,sys; shutil.make_archive(sys.argv[1], 'zip', '.')" "$OLDPWD/$OUT/bond007-$VERSION-windows-$a")
  rm -rf "$d"
done

step "macOS"
for a in arm64 amd64; do
  b=$(build darwin $a); d=$(mktemp -d)/Bond007; mkdir -p "$d"
  cp "$b" "$d/"; cp packaging/macos/*.command "$d/"
  tar -C "$(dirname "$d")" -czf "$OUT/bond007-$VERSION-macos-$a.tar.gz" Bond007
done

step "Linux"
for a in amd64 arm64; do
  b=$(build linux $a); d=$(mktemp -d)/bond007-$VERSION-linux-$a; mkdir -p "$d"
  cp "$b" packaging/linux/install.sh "$d/"
  tar -C "$(dirname "$d")" -czf "$OUT/bond007-$VERSION-linux-$a.tar.gz" "$(basename "$d")"
  for p in deb rpm archlinux; do
    pkg packaging/nfpm-linux.yaml $p "$b" $a
  done
done

step "OpenWrt"
# OpenWrt arch name -> Go target
declare -A OWRT=(
  [aarch64_cortex-a53]="arm64"    # GL.iNet Beryl AX and most Wi-Fi 6 routers
  [aarch64_cortex-a53_neon-vfpv4]="arm64"  # GL.iNet Slate AX (GL-AXT1800, ipq60xx) reports this name
  [aarch64_generic]="arm64"
  [arm_cortex-a7_neon-vfpv4]="arm v7"
  [mipsel_24kc]="mipsle"
  [x86_64]="amd64"
)
for oa in "${!OWRT[@]}"; do
  read -r ga gx <<<"${OWRT[$oa]}"
  b=$(build linux $ga $gx)
  pkg packaging/nfpm-openwrt.yaml ipk "$b" "$oa"
done

step "Server"
for a in amd64 arm64; do
  GOOS=linux GOARCH=$a go build -trimpath -ldflags "$LDFLAGS" -o "dist/bin/server-linux-$a/bond007-server" ./cmd/bond007-server
  tar -C "dist/bin/server-linux-$a" -czf "$OUT/bond007-server-$VERSION-linux-$a.tar.gz" bond007-server
done
if command -v docker >/dev/null && docker info >/dev/null 2>&1; then
  docker build -q --build-arg VERSION="$VERSION" -t "bond007-server:$VERSION" -t bond007-server:latest . >/dev/null
  echo "docker image bond007-server:$VERSION"
else
  echo "docker not available: skipped image (Dockerfile is ready)"
fi

step "Checksums"
(cd "$OUT" && sha256sum * > SHA256SUMS)
ls -lh "$OUT" | awk 'NR>1 {printf "  %-58s %s\n", $9, $5}'
