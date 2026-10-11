# Prior art

This survey is from the author's working knowledge of these projects as of 2026. Verify details against each project's current documentation before relying on them.

| Project | Approach | What Bond007 takes from it |
|---|---|---|
| **Speedify** (Connectify, commercial) | A proprietary UDP tunnel with per-packet bonding across all interfaces. It has a "redundant" mode (duplicate everything), a "speed" mode, and "streaming" mode, which adapts redundancy for detected real-time flows. Clients on Windows, macOS, Linux, iOS and Android. | Packet-level bonding over UDP; per-mode redundancy; mirroring real-time media automatically (`realtimeMirror`); live per-link latency, loss and usage UI. |
| **Peplink SpeedFusion** (commercial routers) | Bonded VPN with "Bandwidth Bonding", "WAN Smoothing" (duplicate packets across WANs) and "Hot Failover". | WAN-smoothing equals our mirror and redundancy modes. The router-first deployment informed our OpenWrt package. |
| **OpenMPTCProuter** (open source, OpenWrt) | Aggregates WANs with kernel MPTCP to a VPS, plus Shadowsocks/Glorytun/OpenVPN for non-TCP. | Proves router plus VPS is a practical topology. We avoid kernel MPTCP so it runs unmodified on Windows, macOS and stock OpenWrt. |
| **Engarde** (open source, Go) | Duplicates every WireGuard packet over all interfaces; the receiver drops duplicates. | The "send on all, first copy wins" model, which is our mirror mode and dedup window. |
| **Glorytun / MUD** | Multipath UDP tunnel with per-path rate and latency awareness. | Per-path measurements driving scheduling. |
| **MLVPN** | Multi-link VPN using weighted round robin over links, with fallback. | Weighted strategy and backup-only links. |
| **Bondix** (commercial, OpenWrt-based) | Packet-level bonding with per-link congestion control and latency-aware scheduling. | Per-link pacing, so slow links don't overflow, plus latency-aware scheduling. |
| **MPTCP / MP-QUIC** | Transport-level multipath with per-subflow congestion control and schedulers such as minRTT/BLEST. | The "fastest arrival" (minRTT-style) scheduler and the insight that each path needs its own congestion control. |
| **WireGuard / wireguard-go / Tailscale** | Cross-platform TUN (Wintun, utun, Linux tun with GSO), the replay window, `IP_UNICAST_IF`/`IP_BOUND_IF` binding. | We use `wireguard-go/tun` directly; our dedup window mirrors WireGuard's replay counter. |
| **tun2socks / gVisor netstack** | Terminate IP flows in a userspace stack. | Lets the server run as an unprivileged container with no NET_ADMIN, which suits Azure Container Instances. |

## Lessons that shaped the design (and that the lab confirmed)

1. **Naive duplication hurts at speed.** TCP converges on the fastest path, so mirrored copies overflow slower links. Per-link pacing is mandatory, not optional.
2. **Round-robin striping across very different latencies destroys TCP.** We measured 98 s versus 4 s for 50 MB. Commercial bonders schedule by estimated arrival time, and so do we by default.
3. **Failover speed is decided by how fast you stop using a dead link**, not by probes. Watch for silence on links that normally carry traffic.
4. **Random loss is not congestion.** Wi-Fi and LTE drop packets without being full; backing off on small loss starves good links.
