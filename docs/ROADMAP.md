# Roadmap and known limits

## Round 2 candidates

- **Encryption and authentication** (see SECURITY.md).
- **IPv6 inside the tunnel.** The client currently routes only IPv4 into the bond, so IPv6 traffic bypasses it. Either assign a ULA/GUA and forward v6, or block v6 while bonded to prevent leaks.
- **Aggregate beyond the fastest link for a single TCP flow.** In the lab, every mode saturates the fastest path (~106 of 120 Mbit). Exceeding it with one flow needs more aggressive overflow onto slower paths, plus a larger reorder budget sized from measured delay spread. Multiple parallel flows already spread naturally.
- **Active speed test per link**, to learn capacity at connect time instead of during the first transfer.
- **TCP fallback transport** for networks that block UDP, and for Azure Container Apps, whose ingress is TCP-only.
- **Batch I/O.** Use `sendmmsg`/`recvmmsg` and GSO on the UDP sockets, and avoid a copy per link on the send path. This matters above ~1 Gbit/s and on router CPUs.
- **macOS DNS override** (scutil). Linux uses resolvectl and Windows uses netsh; macOS currently leaves DNS alone.
- **Signed installers:** Apple notarisation (.pkg), an Authenticode-signed MSI/EXE with an embedded version and icon manifest, and an apk-format package for OpenWrt 24.10+ / 25.x (opkg-format ipk is built today).
- **Per-application rules:** bond only some apps, or bypass the bond for some destinations.
- **ICMP on Azure Container Instances:** ACI drops outbound ICMP, so `ping` through the bond gets no reply when the server runs on ACI (TCP/UDP are unaffected). An Azure VM, or any Docker host, forwards ping fine.
- **Investigate** a brief per-link receive-rate spike shown in the dashboard during the first seconds after a link joins (observed once in the lab; the counters were correct).

## Platform notes

- **Windows:** the Wintun driver (signed by WireGuard LLC, v0.14.1) is embedded in the exe and extracted next to it. The exe self-elevates to install itself as an auto-start service with restart-on-failure. Settings and logs live in `C:\ProgramData\Bond007\`.
- **macOS:** uses a utun device and a launchd daemon. The binary is unsigned, so the installer removes the quarantine attribute.
- **Linux:** systemd unit. Needs `iproute2`, which is present everywhere.
- **OpenWrt:** procd service, uci firewall zone, `kmod-tun` dependency. Uses busybox or full `ip`, and `nft` or `iptables`.
- **Linux link binding** relies on each interface having its own default route (NetworkManager and netifd both do this, with metrics). An interface without one shows a link error in the UI.
