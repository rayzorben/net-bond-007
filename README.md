# Bond007

**Bond every connection you have (Ethernet, Wi-Fi, USB-tethered phones, cellular modems, Bluetooth) into one faster, more reliable link.**

Bond007 is a client and a server. The client runs on Windows, macOS, Linux and OpenWrt routers (GL.iNet Slate AX and others). It creates a virtual network adapter, sends your traffic over all of your connections at once, and constantly measures each one's latency, jitter, loss and capacity. The server runs as a Docker container (Azure, any VPS). It accepts every copy, keeps the **first one to arrive** and discards the rest, makes the real request to the internet, and sends the response back the same way.

```
 apps ──► [virtual adapter] ──► Bond007 client ══ eth  ══╗
                                              ══ wifi ══╬══► Bond007 server ──► internet
                                              ══ usb  ══╝    (first copy wins)
```

## Highlights

- **First copy wins in both directions.** Each packet gets a sequence number. Copies that arrive later over slower links are discarded by a sliding-window filter.
- **Smart redundancy.** With one or two links every packet is mirrored. With three or more, each packet travels over two (configurable) links. Small packets (ACKs, DNS, keystrokes), TCP handshakes and real-time media (Teams, Meet, WebRTC) are always mirrored.
- **Fastest-arrival scheduling.** Each copy goes where it will land soonest, counting latency plus whatever is already queued on the link. Weighted round-robin is available too; it reproduces the brief's `{1,2} {2,3} {1,3}` rotation exactly.
- **Adaptive per-link pacing.** Each link's real capacity is learned from RTT inflation, delivery rate and loss, so slow links never drown in duplicates.
- **Instant failover.** A busy link that goes silent is dropped from scheduling within roughly 8 one-way delays (minimum 120 ms), before any probe times out.
- **Reorder buffer.** It holds early packets briefly so TCP sees them in order. The hold time is sized automatically from the latency spread between links.
- **Userspace internet gateway (gVisor).** The server needs no `NET_ADMIN` or `/dev/net/tun`, so it runs as an unprivileged container on any Docker host.
- **A dashboard you'll actually look at.** It has a live animated bond map with packets racing over each link and the winners flashing gold, plus per-link quality gauges, sparklines, drill-down charts and raw counters. **Every setting is in the UI** (advanced ones behind a toggle), and every change applies live with no restart.

## Install

| Platform | File | How |
|---|---|---|
| Windows 10/11 (x64, ARM) | `bond007-<v>-windows-*.zip` | Double-click `bond007.exe` and accept the admin prompt. It installs as a service and opens the dashboard. |
| macOS (Apple Silicon, Intel) | `bond007-<v>-macos-*.tar.gz` | Double-click `Install Bond007.command`. |
| Debian/Ubuntu | `bond007_<v>_amd64.deb` | `sudo apt install ./bond007_*.deb` |
| Fedora/RHEL | `bond007-<v>-1.x86_64.rpm` | `sudo dnf install ./bond007-*.rpm` |
| Arch/CachyOS | `bond007-<v>-1-x86_64.pkg.tar.zst` | `sudo pacman -U bond007-*.pkg.tar.zst` |
| Any Linux | `bond007-<v>-linux-*.tar.gz` | `sudo ./install.sh` |
| **GL.iNet Slate AX** | `bond007_<v>_aarch64_cortex-a53.ipk` | See [OpenWrt](#openwrt--glinet-slate-ax) |

Then open **http://127.0.0.1:7007**, enter your server address and token, and you're bonded. From a terminal you can do the same in one step:

```bash
sudo bond007 install --server bond.example.com:7007 --token YOUR_TOKEN
```

### OpenWrt / GL.iNet Slate AX

```bash
scp bond007_0.1.0_aarch64_cortex-a53.ipk root@192.168.8.1:/tmp/
ssh root@192.168.8.1 'opkg update && opkg install /tmp/bond007_0.1.0_aarch64_cortex-a53.ipk'
```

The package installs a procd service. It also adds a `bond007` firewall zone with masquerading and MSS clamping, and forwards `lan` into it. Every device on your LAN is then bonded. The dashboard is at `http://192.168.8.1:7007`; **set a password** in *Settings › Dashboard*. WAN, the Wi-Fi repeater (STA), USB tethering and cellular modems are bonded automatically, while LAN bridges are ignored. Packages are also built for `aarch64_generic`, `arm_cortex-a7_neon-vfpv4`, `mipsel_24kc` and `x86_64`.

> GL.iNet's own multi-WAN (kmwan/mwan3) also manages routes. If it fights Bond007, set it to failover-only or turn it off.

## Server

```bash
docker run -d --name bond007 --restart unless-stopped \
  -p 7007:7007/udp -p 8080:8080 \
  -e BOND007_TOKEN=change-me -e BOND007_UI_PASSWORD=change-me-too \
  -v bond007-data:/data bond007-server:0.1.0
```

- **Production (orohost):** the server runs in Docker on `orohost`, the home server, as image [`rayzorben/bond007-server`](https://hub.docker.com/r/rayzorben/bond007-server). The container is declared with [stay-go](../stay-go), not here: see `stay-go/config/hosts/orohost.yaml` (the `compose:` entry and `secrets.bond007.*`) and `stay-go/config/files/docker/bond007/`. Clients connect to the home public IP on **UDP 7007**, which the home router forwards to 192.168.1.50. The dashboard is at `http://192.168.1.50:8077`, reachable on the LAN or over Tailscale (qbittorrent owns 8080). Server config lives in `/storage/apps/bond007/server.json`. The token and dashboard password are in `deploy/orohost/.bond007.env` (git-ignored) and, encrypted, in stay-go.

  **To ship an update:** run `deploy/orohost/deploy.sh`. It builds the image, pushes `:<source-hash>` and `:latest` to Docker Hub, then runs `stay-go --update=compose/bond007` on orohost, which pulls the image and recreates the container only if it changed. You need `docker login -u rayzorben` on this machine and on orohost. To change ports, env or volumes, edit the stay-go files instead and run `stay-go -c config --apply=compose/bond007` on orohost from `/storage/dropbox/Dropbox/PC/development/stay-go`. Use that path, not `~/Dropbox`, because stay-go state records it. Roll back by retagging an older `:<hash>` as `:latest`, or pin the tag in the compose file.
- **Compose:** `deploy/docker-compose.yml`.
- **Dashboard:** `http://<server>:8080` shows connected clients, each with its own bond map, plus egress connection stats.

## Build

```bash
scripts/build.sh            # UI, tests, every package, Docker image -> dist/release
SKIP_TESTS=1 VERSION=0.2.0 scripts/build.sh
```

You need Go 1.27+, Node 20+, and optionally Docker. Everything else (nfpm, the signed Wintun driver) is fetched or embedded automatically.

## Test

```bash
go test ./...                        # unit tests + in-process end-to-end tests (no root needed)
sudo scripts/netns-lab.sh up         # real TUN lab: 3 emulated links (26/46/100 ms, 120/80/30 Mbit)
sudo scripts/lab-bench.sh smart speed mirror
sudo scripts/netns-lab.sh cut eth1   # pull a cable mid-download
sudo scripts/netns-lab.sh down
```

The end-to-end tests run a real server and client over loopback with impairment proxies, and use a userspace TCP stack as the "OS". They download multi-megabyte files over four lossy links and verify SHA-256 integrity, kill a link mid-transfer, change settings live and reject bad tokens.

## Docs

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md): protocol, engine, scheduling, pacing, failover
- [docs/RESEARCH.md](docs/RESEARCH.md): prior art (Speedify, Peplink, OpenMPTCProuter, Engarde, …) and what we took from each
- [docs/SECURITY.md](docs/SECURITY.md): **traffic is not encrypted yet**; read this first
- [docs/ROADMAP.md](docs/ROADMAP.md): known limits and next rounds
