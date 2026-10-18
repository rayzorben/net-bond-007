# Architecture

## Pieces

| Package | Role |
|---|---|
| `internal/proto` | Wire format. A common 8-byte header, DATA frames (+8-byte sequence number + raw IP packet), binary PROBE/PROBE_ACK, NACK and FEC (parity) frames, and JSON control frames (HELLO, HELLO_ACK, POLICY, REJECT, BYE). |
| `internal/dedup` | First-copy-wins filter: a 16 384-packet sliding bitmap window (IPsec/WireGuard style), O(1) per packet. |
| `internal/sched` | Decides how many copies each packet gets (`Copies`) and which links carry them (`PickFastest` / weighted `Pick`). |
| `internal/reorder` | Holds early packets until the gap fills or the hold time expires. It is a pure data structure driven by the caller's clock. |
| `internal/metrics` | RTT/jitter (RFC 6298), probe loss, rate meters with decaying peak, history rings, a quality score. |
| `internal/bond` | The engine shared by both ends: links, send path, receive path, pacer, failover, views. |
| `internal/client` | Interface discovery and reconciliation, one UDP socket per interface pinned to it, probes, tunnel lifecycle. |
| `internal/server` | One UDP listener for all clients (with IP_PKTINFO so replies leave from the right address), sessions, IP pool. |
| `internal/netstack` | The server's internet gateway: the gVisor userspace TCP/IP stack. TCP and UDP flows are terminated and re-originated with real sockets, and pings are forwarded. |
| `internal/platform` | OS specifics: Wintun/utun/tun via `wireguard-go/tun`; `SO_BINDTODEVICE` / `IP_BOUND_IF` / `IP_UNICAST_IF`; routes; service install. |
| `internal/config` | Every setting carries struct-tag metadata. The schema is reflected for the UI, and a test fails if any setting is missing from it. |
| `internal/webui` + `web/` | JSON API, Server-Sent Events, login, and the embedded Svelte 5 dashboard. |

## Packet path

**Upstream.** An app writes to a socket. The OS routes it into the virtual adapter (routes `0.0.0.0/1` + `128.0.0.0/1` beat the default route without replacing it). The client engine assigns `seq = n++`. The scheduler picks *k* links, and each copy is framed and queued on its link, where a writer goroutine sends it from a socket bound to that interface. The server's dedup window accepts the first copy and drops the rest. The packet is anti-spoof checked (its source must be the client's tunnel IP) and injected into gVisor. gVisor's TCP/UDP forwarders terminate the flow and `net.Dial` the real destination.

**Downstream.** The remote replies to the server's real socket. gVisor turns the bytes into IP packets addressed to the client's tunnel IP. The session engine schedules them over the client's links using the policy the client pushed (POLICY frames). The client dedups, reorders, and writes to the adapter.

TLS/QUIC stay end-to-end between your apps and the sites they talk to. The server never decrypts anything and doesn't need to.

## Scheduling

1. `Eligible`: links that are up/degraded, not congested, and not *silent*. Primaries come first; backup links are used only if no primary qualifies.
2. `Copies`: smart mode means all links when there are ≤2, otherwise `redundancy`. Mirror means all links, speed means 1, custom means N. Packets ≤ `smallPacketMirrorBytes`, TCP SYN/FIN/RST, ICMP and real-time UDP ports always go on every link.
3. Pacing filter: only links with token-bucket budget carry a copy. If none has budget, one copy still goes on the best link (the bucket goes into debt), so TCP's own congestion control stays in charge.
4. Strategy:
   - **Fastest arrival** (default): `delay = one-way latency + pacer debt + queue/rate`. Choose the k smallest. Fast links fill first and slow links absorb overflow, which keeps reordering small.
   - **Weighted round robin**: smooth WRR extended to k distinct picks, with weights proportional to learned capacity × a latency factor. Three equal links with k=2 give `{1,2} {1,3} {2,3}` (the scenario in the original brief; see `TestPickRotatesPairsLikeTheBrief`).

## Pacing (why it exists)

With redundancy, TCP only feels the fastest path because its copy always wins. TCP ramps up to that speed and overflows the slower links with duplicates. Their queues bloat to seconds and drop everything, so when the fast path loses a packet the "backup" copy is useless and TCP stalls in retransmit timeouts. We measured exactly this: 3–4 s stalls and 230 K netem drops.

Each link therefore has an AIMD pacer:
- **back off ×0.8** when RTT rises more than `pacingDelayMs` (default 15 ms, or half the base RTT if larger) above its minimum, when probe loss exceeds `pacingLossPct` (default 10%; Wi-Fi loses packets at random, so loss alone is a weak signal), or when delivery has fallen short of sending (<75%) for more than 1 s (a BBR-style delivery-rate check);
- **grow** by 25% per update while uncongested for 3 s, or 8% near capacity, but never beyond 1.5× actual use or 1.4× the far end's measured delivery rate;
- **remember** the learned rate through idle periods.

The learned rate is also the link's capacity estimate for weights and the dashboard.

## Loss recovery: ARQ and FEC

The inner TCP connections from the server's userspace stack (gVisor, CUBIC) cross the bond, so any loss they see costs them their window. Two mechanisms hide loss from them:

- **ARQ** (`bonding.retransmit`): the receiver spots a hole in the bond's sequence numbers, NACKs it, and the reorder buffer holds the packets behind it until the repair arrives. Repairs go out on a priority lane ahead of queued data; behind a deep queue they used to land after the hold had already given up.
- **Storm-aware repair:** Starlink has loss storms around some satellite handovers. Captured in the field: 5–7 s windows near the :12/:42 handover slots, with 20–200 ms outages mixed with 10–40% loss. Outside them the link was clean. A repair sent back into the storm was usually lost too, and half the losses needed 2–3 NACK rounds. In one capture the inner TCP then stalled for 2.5 s. The sender now marks a link *storming* when it loses 16 packets within 200 ms, for 1 s, and repairs go on a calm link while one exists. In the field, losses fixed by the first repair rose from 40% to 75%, and send-to-repair fell from a median of 286 ms to 97 ms.
- **Outage reports (QUIET):** during a 20–200 ms outage the receiver can't NACK, because no later packet reveals the gap. The sender's own silence check watches the link's upstream, which a download barely uses. So the receiver watches each busy link (30+ packets per 100 ms): after 50 ms of silence while another link is still heard, it sends QUIET on the live links, repeated every 25 ms. The sender then immediately resends on other links everything it put only on the silent link since the receiver last heard it. It keeps new packets off that link for 75 ms after the last report and treats it as storming. In the field, the p90 time from send to resend fell from 289 ms to 224 ms; the median stayed at about 107 ms.
- **FEC** (`bonding.fec`, on by default): ARQ costs at least one round trip per loss, and the reorder buffer holds in-order delivery for that long. On a link with steady random loss (Starlink), holes open many times a second. In-order delivery is then held most of the time, so the inner TCP's round trip stretches 2–3x and a window sized for the real RTT runs at half speed. So for every group of *k* packets sent on a link, the sender also sends their XOR. The receiver rebuilds a single missing packet as soon as the parity arrives (about 5 ms in the lab instead of 100–290 ms). Groups are interleaved two ways so a short burst lands in different groups. A parity that overtakes its group is kept for up to 100 ms and retried.
  - Sizing is per link and automatic. It counts isolated losses only: NACKed single packets, plus the peer's FEC rebuilds, which come back in a trailing PROBE/PROBE_ACK field so FEC doesn't switch itself off by hiding the loss. FEC turns on at 0.05% loss with *k* = 0.08/loss, clamped to 8..32 (about 3% overhead at 0.2% loss, 12.5% at 1% or more). It turns off after 30 s below 0.02%. Clean links carry no parity. Bursts (a buffer overflowing) are not counted: one parity can't rebuild them.
  - Compatibility: HELLO/HELLO_ACK carry `fec`, and the client asks for downstream FEC in POLICY. Peers that don't advertise it never receive FEC frames, and older peers ignore unknown frame types anyway.

**Switches.** Each mechanism has its own `bonding` setting, so it can be compared against the bond without it: `fec`, `repairPriority`, `stormRepair` and `outageReports` (all on by default), and `lossTolerantTCP` (experimental, off by default). The client pushes them to the server in POLICY. Older clients don't send them, and the server then leaves those features off.

**Loss-tolerant TCP (experimental).** The server's userspace TCP can use `bbr`, a simplified window-based BBR patched into a trimmed copy of gVisor (`third_party/gvisor`, see its PATCHES.md). Status on 2026-10-17: it hasn't yet beaten CUBIC in the lab. Some runs collapsed to a few Mbit/s, so it stays off.

## Health and failover

- Probes go out every 250 ms per link: RTT, jitter (RTTVAR) and loss over the last 40 probes. The client reports its view to the server in every probe, including the latest raw RTT and per-interval delivered rate.
- Link states are connecting → up / degraded (RTT > 300 ms or loss > 3%) / down (silent for > 1.2 s or loss > 60%). All thresholds are settings.
- **Silence detection:** while the bond is busy, a link that normally carries traffic and has heard nothing for `max(8 × one-way delay, 120 ms)` is excluded at once on both ends, without waiting for probe timeouts.
- The server reflects state from client probes and also times links out on its own (`linkTimeoutMs`).
- If the server restarts, a REJECT `unknown-session` triggers re-HELLO. A changed `epoch` in HELLO_ACK resets the client's receive window, because the server's sequence numbers restart.
- **Fail-open:** once the tunnel is up, its routes send everything into it, so a dead server would black-hole the whole network. When no link has been usable for `tunnel.failOpenSec` (default 8 s), the client moves the tunnel's routes straight out of the best physical link (one with a gateway; primary before backup, unmetered before metered, then most reliable, then wired before wireless). Links keep probing the server through their interface-bound sockets. After one has answered for 30 s straight, new connections go back into the tunnel. In the lab, a killed server cost about 11 s of connectivity, and switching back lost no packets. With pinning (below), connections stuck in the dead tunnel are reset so apps reconnect over the bypass at once, and the ones that started on the bypass stay on it after the server returns.
- **Connection pinning (router mode, Linux):** each new LAN connection gets a conntrack mark (bits `0x0f000000`) naming the path it started on: 1 is the tunnel, 2 and up are physical links the bond has gone direct over. Policy rules at pref 5900+N send marked packets to table 7100+N, ahead of GL.iNet's rules (pref 5899 keeps LAN and other specific main routes first). Switching between the bond and going direct changes only the main table's `/1` routes, so only new connections move, and a stream keeps its public IP until it ends. Connections pinned to a path that dies (the tunnel during fail-open, or a direct link unusable for 15 s) get TCP resets so apps reconnect on the current path instead of hanging.
- **Never slower than direct (`bonding.directGuard`):** every `directGuardMinutes` (60), and 3 minutes after the set of usable links changes, the client downloads a test file for up to 6 s through the bond and over the best physical link, twice each, back to back. If the bond gets under 85% of direct, new connections go direct over that link. The bond takes over again once it tests at 105% or more, or as soon as the direct link has been unusable for 5 s.
- **Switching pace:** going direct with one reliable link waits 60 s, going back to the bond needs 300 s of two or more reliable links, and dropping copies waits 30 s (it used to be 15 s, 30 s and 10 s, which flipped paths every few minutes on Starlink next to a phone).

## Live configuration

`config.Store` validates (struct-tag ranges and enums plus custom checks), persists atomically and notifies subscribers. The client re-applies policy to its engine and pushes POLICY to the server, reconciles links, reconfigures or recreates the adapter, and rebinds the dashboard. The server rebinds its UDP listener and dashboard and updates gateway options. Nothing requires a restart, except the server's address pool, MTU and TCP buffer size, which apply to new sessions (logged).

## Lab results (netns, 3 emulated links)

Links: eth 26 ms / 120 Mbit; Wi-Fi 46 ms / 80 Mbit / 0.5% loss each way; USB 100 ms / 30 Mbit / 1% loss each way. Single TCP flow, 50 MB, SHA-256 verified on every run.

| Mode | Throughput (steady) |
|---|---|
| Smart / Speed / Mirror | ~106 Mbit/s, which is the fastest path's ceiling after header overhead. All intact. |
| Wi-Fi alone | ~3.4 Mbit/s. That's the Mathis limit for TCP at ~1% loss; it's not a Net-Bond-007 bottleneck. |
| Wi-Fi + USB mirrored | ~13 Mbit/s, about 4× the single lossy link, because redundancy hides random loss. |
| Ethernet pulled mid-transfer | completes intact; the remaining lossy links then limit TCP as above. |

### Single lossy link (PROFILE=starlink, eth1 only: 40 Mbit, 76 ms RTT)

Direct uses a BBR sender, the way a CDN would. Medians of 5 runs of 50 MB.

| Link condition | Direct | Bonded before FEC | Bonded with FEC + repair priority |
|---|---|---|---|
| Clean | 35.6 | 35.4 | — |
| 0.2% random loss | 36 | 15–19 | 33.2 |
| 0.2% loss + 150-packet buffer + 6 ms jitter | 35.6 | ~14–20 | 23.4 |

In the field, Starlink's loss is bursty rather than random (see storm-aware repair). Single-parity FEC rebuilds few of those losses, so there the remaining gap to direct is the time it takes to detect an outage (the receiver can't NACK until traffic resumes) and the inner TCP's slow ramp afterwards.

What limits the bond on Starlink, measured in the field on 2026-10-17: Starlink loses packets for every sender, not just the bond. Plain TCP downloads over the same dish ran at 3–7 Mbit/s from Hetzner Ashburn and ~14 from the OCI server (both CUBIC), against 73 from Cloudflare (BBR). Inside the bond the sender is gVisor's CUBIC. ARQ, FEC, storm-aware repair and QUIET lift it to 20–30 Mbit/s, but a loss-sensitive sender stays far below what BBR senders reach directly. Closing the gap needs a loss-tolerant inner sender: BBR-like congestion control in the userspace stack, or forwarding flows through the kernel so the original sender's congestion control runs end to end.

On a clean link the bond adds nothing measurable. Random loss was the main bottleneck, and FEC removes it. A shallow buffer is still a limit: bursts overflow it, and probing for capacity overruns it. That is the shaper's job and remains open.
