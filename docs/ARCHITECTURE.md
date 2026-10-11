# Architecture

## Pieces

| Package | Role |
|---|---|
| `internal/proto` | Wire format. A common 8-byte header, DATA frames (+8-byte sequence number + raw IP packet), binary PROBE/PROBE_ACK, and JSON control frames (HELLO, HELLO_ACK, POLICY, REJECT, BYE). |
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

## Health and failover

- Probes go out every 250 ms per link: RTT, jitter (RTTVAR) and loss over the last 40 probes. The client reports its view to the server in every probe, including the latest raw RTT and per-interval delivered rate.
- Link states are connecting → up / degraded (RTT > 300 ms or loss > 3%) / down (silent for > 1.2 s or loss > 60%). All thresholds are settings.
- **Silence detection:** while the bond is busy, a link that normally carries traffic and has heard nothing for `max(8 × one-way delay, 120 ms)` is excluded at once on both ends, without waiting for probe timeouts.
- The server reflects state from client probes and also times links out on its own (`linkTimeoutMs`).
- If the server restarts, a REJECT `unknown-session` triggers re-HELLO. A changed `epoch` in HELLO_ACK resets the client's receive window, because the server's sequence numbers restart.

## Live configuration

`config.Store` validates (struct-tag ranges and enums plus custom checks), persists atomically and notifies subscribers. The client re-applies policy to its engine and pushes POLICY to the server, reconciles links, reconfigures or recreates the adapter, and rebinds the dashboard. The server rebinds its UDP listener and dashboard and updates gateway options. Nothing requires a restart, except the server's address pool, MTU and TCP buffer size, which apply to new sessions (logged).

## Lab results (netns, 3 emulated links)

Links: eth 26 ms / 120 Mbit; Wi-Fi 46 ms / 80 Mbit / 0.5% loss each way; USB 100 ms / 30 Mbit / 1% loss each way. Single TCP flow, 50 MB, SHA-256 verified on every run.

| Mode | Throughput (steady) |
|---|---|
| Smart / Speed / Mirror | ~106 Mbit/s, which is the fastest path's ceiling after header overhead. All intact. |
| Wi-Fi alone | ~3.4 Mbit/s. That's the Mathis limit for TCP at ~1% loss; it's not a Bond007 bottleneck. |
| Wi-Fi + USB mirrored | ~13 Mbit/s, about 4× the single lossy link, because redundancy hides random loss. |
| Ethernet pulled mid-transfer | completes intact; the remaining lossy links then limit TCP as above. |
