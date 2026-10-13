# Net-Bond-007

**Bond every connection you have (Ethernet, Wi-Fi, USB-tethered phones, cellular modems, Bluetooth) into one faster, more reliable link.**

Net-Bond-007 is a client and a server. The client runs on Windows, macOS, Linux and OpenWrt routers (GL.iNet Slate AX and others). It creates a virtual network adapter, sends your traffic over all of your connections at once, and constantly measures each one's latency, jitter, loss and capacity. The server runs as a Docker container (Azure, any VPS). It accepts every copy, keeps the **first one to arrive** and discards the rest, makes the real request to the internet, and sends the response back the same way.

```
 apps ──► [virtual adapter] ──► Net-Bond-007 client ══ eth  ══╗
                                              ══ wifi ══╬══► Net-Bond-007 server ──► internet
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
| Windows 10/11 (x64, ARM) | `net-bond-007-<v>-windows-*.zip` | Double-click `net-bond-007.exe` and accept the admin prompt. It installs as a service and opens the dashboard. |
| macOS (Apple Silicon, Intel) | `net-bond-007-<v>-macos-*.tar.gz` | Double-click `Install Net-Bond-007.command`. |
| Debian/Ubuntu | `net-bond-007_<v>_amd64.deb` | `sudo apt install ./net-bond-007_*.deb` |
| Fedora/RHEL | `net-bond-007-<v>-1.x86_64.rpm` | `sudo dnf install ./net-bond-007-*.rpm` |
| Arch/CachyOS | `net-bond-007-<v>-1-x86_64.pkg.tar.zst` | `sudo pacman -U net-bond-007-*.pkg.tar.zst` |
| Any Linux | `net-bond-007-<v>-linux-*.tar.gz` | `sudo ./install.sh` |
| **GL.iNet Slate AX** | `net-bond-007_<v>_aarch64_cortex-a53.ipk` | See [OpenWrt](#openwrt--glinet-slate-ax) |

Then open **http://127.0.0.1:7007**, enter your server address and token, and you're bonded. From a terminal you can do the same in one step:

```bash
sudo net-bond-007 install --server bond.example.com:7007 --token YOUR_TOKEN
```

### OpenWrt / GL.iNet Slate AX

```bash
scp net-bond-007_0.1.0_aarch64_cortex-a53.ipk root@192.168.8.1:/tmp/
ssh root@192.168.8.1 'opkg update && opkg install /tmp/net-bond-007_0.1.0_aarch64_cortex-a53.ipk'
```

The package installs a procd service. It also adds a `net-bond-007` firewall zone with masquerading and MSS clamping, and forwards `lan` into it. Every device on your LAN is then bonded. The dashboard is at `http://192.168.8.1:7007`; **set a password** in *Settings › Dashboard*. WAN, the Wi-Fi repeater (STA), USB tethering and cellular modems are bonded automatically, while LAN bridges are ignored. Packages are also built for `aarch64_generic`, `arm_cortex-a7_neon-vfpv4`, `mipsel_24kc` and `x86_64`.

> GL.iNet's own multi-WAN (kmwan/mwan3) also manages routes. If it fights Net-Bond-007, set it to failover-only or turn it off.

## Server

The server is a single unprivileged Docker container. It needs no `NET_ADMIN`, no `/dev/net/tun` and no host networking, so it runs anywhere Docker runs: a VPS, a home server, Azure, AWS, GCP, and so on.

| Port | Protocol | Purpose | Expose to |
|---|---|---|---|
| `7007` | **UDP** | Bond traffic from clients | The internet (your clients) |
| `8080` | TCP | Dashboard | Only yourself (your IP, VPN or SSH tunnel) |

| Variable | Required | Meaning |
|---|---|---|
| `NET_BOND_007_TOKEN` | yes | Shared token that clients must present |
| `NET_BOND_007_UI_PASSWORD` | yes | Dashboard password |
| `NET_BOND_007_NAME` | no | Server name shown in the dashboard |
| `NET_BOND_007_LISTEN` | no | Bond listen address (default `:7007`) |
| `NET_BOND_007_UI_LISTEN` | no | Dashboard listen address (default `:8080`) |
| `NET_BOND_007_DNS` | no | Upstream DNS for egress |

Config is stored in `/data/server.json` inside the container. Mount a volume there so it survives restarts. Every setting can also be changed live from the dashboard.

### 1. Build the image

```bash
docker build -t net-bond-007-server .
```

To run it on another machine, tag it and push it to a registry you control (Docker Hub, GHCR, ACR, ECR):

```bash
docker tag net-bond-007-server <registry>/net-bond-007-server:latest
docker push <registry>/net-bond-007-server:latest
```

### 2. Run it

Generate strong secrets first:

```bash
export NET_BOND_007_TOKEN=$(openssl rand -hex 24)
export NET_BOND_007_UI_PASSWORD=$(openssl rand -base64 18)
```

**docker run:**

```bash
docker run -d --name net-bond-007 --restart unless-stopped \
  -p 7007:7007/udp -p 8080:8080 \
  -e NET_BOND_007_TOKEN -e NET_BOND_007_UI_PASSWORD \
  -v net-bond-007-data:/data net-bond-007-server
```

**docker compose** (builds from source and reads the two variables from your shell or a `.env` file next to the compose file):

```bash
docker compose -f deploy/docker-compose.yml up -d --build
```

Then point clients at `<server-public-ip-or-dns>:7007` with the token, and open the dashboard at `http://<server>:8080`.

To update, rebuild or pull the new image and recreate the container. Settings persist in the `/data` volume.

### Cloud hosts

The checklist is the same on every provider:
1. Allow **inbound UDP 7007** from anywhere.
2. Allow TCP 8080 only from your own IP, or reach it over a VPN or SSH tunnel.
3. Give the host a static public IP or a DNS name.

**Any VPS** (DigitalOcean, Hetzner, Linode, Vultr, Oracle Cloud, …): install Docker, run the commands above, and open UDP 7007 in the provider firewall and in `ufw`/`firewalld` if you use one. This is the simplest and usually the cheapest option.

**AWS**
- **EC2** (recommended): launch a small Linux instance, attach an Elastic IP, and add security group rules for UDP 7007 (`0.0.0.0/0`) and TCP 8080 (your IP). Then install Docker and run the container as above.
- **ECS on Fargate**: push the image to ECR and create a task with port mappings `7007/udp` and `8080/tcp`. Set the two variables (preferably from Secrets Manager) and mount EFS at `/data`. Expose UDP 7007 through a **Network Load Balancer** with a UDP listener, or run the task with a public IP. Application Load Balancers do not support UDP.

**Azure**
- **VM** (recommended): create a Linux VM with a static public IP. Add NSG inbound rules for UDP 7007 and TCP 8080 (your IP), then install Docker and run the container.
- **Azure Container Instances**: push to ACR (or use any registry), then:

  ```bash
  az container create -g <rg> -n net-bond-007 \
    --image <registry>/net-bond-007-server:latest \
    --ports 7007 8080 --protocol UDP --ip-address Public --dns-name-label <name> \
    --secure-environment-variables NET_BOND_007_TOKEN=$NET_BOND_007_TOKEN NET_BOND_007_UI_PASSWORD=$NET_BOND_007_UI_PASSWORD \
    --azure-file-volume-share-name <share> --azure-file-volume-account-name <account> \
    --azure-file-volume-account-key <key> --azure-file-volume-mount-path /data
  ```

  The CLI sets one protocol for every port. To get UDP 7007 and TCP 8080 together, deploy from a YAML or ARM file that lists each port with its own protocol, or skip the dashboard port and use the VM option.
- **Azure Container Apps**: supports TCP ingress only, with no UDP, so it **cannot** host the bond port. Use a VM or ACI.

**Google Cloud**: a Compute Engine VM, either with Docker or with the "deploy container" option, plus firewall rules for UDP 7007 and TCP 8080. Cloud Run does not support UDP.

**Kubernetes**: run a Deployment with a PVC at `/data` and a `LoadBalancer` Service with `protocol: UDP` on port 7007. Expose 8080 separately, or reach it with `kubectl port-forward`.

**Home server**: run the container as above, forward **UDP 7007** on your router to the host, and use your public IP or a dynamic DNS name as the server address.

> Traffic between client and server is **not encrypted yet** (only the token is checked). See [docs/SECURITY.md](docs/SECURITY.md) before you expose a server.

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
