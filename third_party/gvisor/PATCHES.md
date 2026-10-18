# gVisor, trimmed and patched for Net-Bond-007

Upstream: `gvisor.dev/gvisor` at the version in `UPSTREAM_VERSION`. The root
`go.mod` replaces the module with this directory.

## What is here

Only the packages Net-Bond-007 builds, for every target in `scripts/build.sh`,
plus what `go mod tidy` needs. Upstream `_test.go` files are left out.

To refresh from a new upstream version:

1. Bump the version in the root `go.mod` `require`, and remove the `replace`.
2. Re-copy the package list from `go list -deps` for each GOOS/GOARCH target.
3. Copy `pkg/goid` too (tidy needs it), then re-apply the patches below.

## Patches

1. **`pkg/tcpip/transport/tcp/bbr.go`** (new) adds `bbr`, a simplified,
   window-based BBR. It keeps cwnd at 3x the bandwidth-delay product
   (windowed max delivery rate x windowed min RTT) and doesn't cut it on
   loss. It has ProbeRTT (drain to 4 packets for 200 ms every 10 s), ends
   startup once RTT doubles, takes rate samples over max(send, ack) interval,
   and floors ssthresh after a loss or RTO. The tests are in `bbr_test.go`
   (window convergence, bounded queue, ACK compression, RTO recovery).
   - `protocol.go`: registers `ccBBR = "bbr"` in `availableCongestionControl`.
   - `snd.go`: `initCongestionControl` handles `ccBBR`. The RFC 5681 idle
     restart (cwnd back to IW after idle > RTO) is skipped for `bbr`.
   - It is used per connection by `internal/netstack` when the client's
     `bonding.lossTolerantTCP` setting is on. It's **experimental and off
     by default**.
2. **`pkg/tcpip/transport/tcp/sack_scoreboard.go`**: three `header.SACKBlock`
   literals are keyed. That's cosmetic; it keeps `go vet ./...` clean, since
   vet also checks this local module.
