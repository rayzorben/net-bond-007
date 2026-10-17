// Package netstack is the server's internet gateway. It runs gVisor's
// userspace TCP/IP stack: client IP packets are injected into it, every TCP
// connection and UDP flow is terminated there, and the server opens a real
// socket to the destination ("the server makes the request"). Responses go
// back through the stack, which turns them into IP packets for the bond.
//
// Running the stack in userspace means the container needs no NET_ADMIN
// capability, no /dev/net/tun and no iptables. That lets it run unprivileged
// on Azure Container Instances, App Service, Kubernetes and so on.
//
// TLS (HTTPS, QUIC) is end-to-end between the client's application and the
// remote site. The gateway forwards encrypted bytes and never sees plaintext.
// Nothing needs decrypting for bonding to work.
package netstack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// Options are read on every new flow, so changes apply live.
type Options struct {
	DNSUpstream    string // host:port; "" = system resolver
	AllowPrivate   bool
	TCPDialTimeout time.Duration
	UDPIdleTimeout time.Duration
	TCPIdleTimeout time.Duration
	ICMPEcho       bool
}

// Dialer opens the real outbound connection; replaceable in tests.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

type Stats struct {
	TCPActive, TCPTotal, TCPFailed atomic.Int64
	UDPActive, UDPTotal            atomic.Int64
	Blocked, Pings                 atomic.Int64
	BytesUp, BytesDown             atomic.Int64
}

type Gateway struct {
	stack   *stack.Stack
	ep      *channel.Endpoint
	gw      netip.Addr
	opts    atomic.Pointer[Options]
	out     func(dst netip.Addr, pkt []byte)
	dial    atomic.Pointer[Dialer]
	ctx     context.Context
	cancel  context.CancelFunc
	log     *slog.Logger
	Stats   Stats
	limitMu sync.Mutex
	perIP   map[netip.Addr]int
	MaxPer  atomic.Int64
	eps     sync.Map // tcpip.Endpoint -> struct{}: live inner TCP connections
}

// New creates a gateway. gateway is the tunnel-side address of the server
// (DNS queries to gateway:53 are forwarded to the upstream resolver). out is
// called with every IP packet destined to a client.
func New(gateway netip.Addr, mtu int, tcpBuf int, opts Options, out func(dst netip.Addr, pkt []byte), log *slog.Logger) (*Gateway, error) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4},
	})
	ep := channel.New(4096, uint32(mtu), "")
	if err := s.CreateNIC(1, ep); err != nil {
		return nil, errors.New(err.String())
	}
	s.SetPromiscuousMode(1, true)
	s.SetSpoofing(1, true)
	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: 1},
		{Destination: header.IPv6EmptySubnet, NIC: 1},
	})
	sack := tcpip.TCPSACKEnabled(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
	// CUBIC recovers from loss bursts (a link dying mid-transfer) much
	// faster than gVisor's default Reno; RACK copes with the reordering
	// inherent in multipath delivery.
	cc := tcpip.CongestionControlOption("cubic")
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &cc)
	rack := tcpip.TCPRecovery(tcpip.TCPRACKLossDetection)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &rack)
	mod := tcpip.TCPModerateReceiveBufferOption(true)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &mod)
	if tcpBuf < 64<<10 {
		tcpBuf = 64 << 10
	}
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: tcpBuf}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv)
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: tcpBuf}
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &snd)

	ctx, cancel := context.WithCancel(context.Background())
	g := &Gateway{
		stack: s, ep: ep, gw: gateway, out: out, ctx: ctx, cancel: cancel, log: log,
		perIP: map[netip.Addr]int{},
	}
	g.MaxPer.Store(20000)
	g.SetDialer((&net.Dialer{}).DialContext)
	g.SetOptions(opts)

	tcpFwd := tcp.NewForwarder(s, 0, 4096, g.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(s, g.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	go g.outLoop()
	return g, nil
}

func (g *Gateway) SetOptions(o Options) { g.opts.Store(&o) }
func (g *Gateway) SetDialer(d Dialer)   { g.dial.Store(&d) }

func (g *Gateway) Close() {
	g.cancel()
	g.stack.Close()
}

// Inject hands one IP packet from a client to the stack.
func (g *Gateway) Inject(pkt []byte) {
	if len(pkt) < 20 {
		return
	}
	var proto tcpip.NetworkProtocolNumber
	switch pkt[0] >> 4 {
	case 4:
		proto = ipv4.ProtocolNumber
		if pkt[9] == 1 && g.opts.Load().ICMPEcho && g.handleICMP(pkt) {
			return
		}
	case 6:
		proto = ipv6.ProtocolNumber
	default:
		return
	}
	pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
	g.ep.InjectInbound(proto, pb)
	pb.DecRef()
}

func (g *Gateway) outLoop() {
	for {
		pb := g.ep.ReadContext(g.ctx)
		if pb == nil {
			return
		}
		v := pb.ToView()
		b := v.AsSlice()
		var dst netip.Addr
		if len(b) >= 20 && b[0]>>4 == 4 {
			dst = netip.AddrFrom4([4]byte(b[16:20]))
		} else if len(b) >= 40 && b[0]>>4 == 6 {
			dst = netip.AddrFrom16([16]byte(b[24:40]))
		}
		if dst.IsValid() {
			g.out(dst, b)
		}
		v.Release()
		pb.DecRef()
	}
}

func toAddrPort(a tcpip.Address, port uint16) netip.AddrPort {
	ip, _ := netip.AddrFromSlice(a.AsSlice())
	return netip.AddrPortFrom(ip.Unmap(), port)
}

// Allowed reports whether a client may reach dst.
func (g *Gateway) Allowed(dst netip.Addr) bool {
	if g.opts.Load().AllowPrivate {
		return true
	}
	return !(dst.IsPrivate() || dst.IsLoopback() || dst.IsLinkLocalUnicast() ||
		dst.IsLinkLocalMulticast() || dst.IsMulticast() || dst.IsUnspecified() ||
		dst.IsInterfaceLocalMulticast()) && !cgnat.Contains(dst)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func (g *Gateway) acquire(client netip.Addr) bool {
	g.limitMu.Lock()
	defer g.limitMu.Unlock()
	if int64(g.perIP[client]) >= g.MaxPer.Load() {
		return false
	}
	g.perIP[client]++
	return true
}

func (g *Gateway) release(client netip.Addr) {
	g.limitMu.Lock()
	if g.perIP[client]--; g.perIP[client] <= 0 {
		delete(g.perIP, client)
	}
	g.limitMu.Unlock()
}

func (g *Gateway) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst := toAddrPort(id.LocalAddress, id.LocalPort)
	src := toAddrPort(id.RemoteAddress, id.RemotePort)
	opts := g.opts.Load()
	if !g.Allowed(dst.Addr()) || !g.acquire(src.Addr()) {
		g.Stats.Blocked.Add(1)
		r.Complete(true)
		return
	}
	defer g.release(src.Addr())

	ctx, cancel := context.WithTimeout(g.ctx, opts.TCPDialTimeout)
	remote, err := (*g.dial.Load())(ctx, "tcp", dst.String())
	cancel()
	if err != nil {
		g.Stats.TCPFailed.Add(1)
		g.log.Debug("tcp dial failed", "dst", dst, "err", err)
		r.Complete(true)
		return
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		remote.Close()
		r.Complete(true)
		return
	}
	r.Complete(false)
	g.eps.Store(ep, struct{}{})
	defer g.eps.Delete(ep)
	ep.SocketOptions().SetKeepAlive(true)
	local := gonet.NewTCPConn(&wq, ep)

	g.Stats.TCPTotal.Add(1)
	g.Stats.TCPActive.Add(1)
	defer g.Stats.TCPActive.Add(-1)
	g.relayTCP(local, remote, opts.TCPIdleTimeout)
}

type closeWriter interface{ CloseWrite() error }

func (g *Gateway) relayTCP(local, remote net.Conn, idle time.Duration) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn, counter *atomic.Int64) {
		defer wg.Done()
		buf := make([]byte, 64<<10)
		for {
			if idle > 0 {
				src.SetReadDeadline(time.Now().Add(idle))
			}
			n, err := src.Read(buf)
			if n > 0 {
				counter.Add(int64(n))
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		if cw, ok := dst.(closeWriter); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
	}
	go cp(remote, local, &g.Stats.BytesUp)
	go cp(local, remote, &g.Stats.BytesDown)
	wg.Wait()
	local.Close()
	remote.Close()
}

func (g *Gateway) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	dst := toAddrPort(id.LocalAddress, id.LocalPort)
	src := toAddrPort(id.RemoteAddress, id.RemotePort)
	opts := g.opts.Load()
	target := dst.String()
	if dst.Addr() == g.gw && dst.Port() == 53 {
		target = opts.DNSUpstream
		if target == "" {
			target = systemDNS()
		}
	} else if !g.Allowed(dst.Addr()) {
		g.Stats.Blocked.Add(1)
		return true
	}
	if !g.acquire(src.Addr()) {
		g.Stats.Blocked.Add(1)
		return true
	}
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		g.release(src.Addr())
		return true
	}
	local := gonet.NewUDPConn(&wq, ep)
	go func() {
		defer g.release(src.Addr())
		defer local.Close()
		remote, err := (*g.dial.Load())(g.ctx, "udp", target)
		if err != nil {
			g.log.Debug("udp dial failed", "dst", target, "err", err)
			return
		}
		defer remote.Close()
		g.Stats.UDPTotal.Add(1)
		g.Stats.UDPActive.Add(1)
		defer g.Stats.UDPActive.Add(-1)
		idle := opts.UDPIdleTimeout
		if target != dst.String() { // DNS: short-lived
			idle = 10 * time.Second
		}
		done := make(chan struct{}, 2)
		pump := func(dst, src net.Conn, counter *atomic.Int64) {
			buf := make([]byte, 65535)
			for {
				src.SetReadDeadline(time.Now().Add(idle))
				n, err := src.Read(buf)
				if err != nil {
					break
				}
				counter.Add(int64(n))
				if _, err := dst.Write(buf[:n]); err != nil {
					break
				}
			}
			done <- struct{}{}
		}
		go pump(remote, local, &g.Stats.BytesUp)
		go pump(local, remote, &g.Stats.BytesDown)
		<-done
		// Unblock the other pump.
		local.SetReadDeadline(time.Now())
		remote.SetReadDeadline(time.Now())
		<-done
	}()
	return true
}

var _ io.Closer = (*gonet.TCPConn)(nil)

// TCPInfo describes each live inner TCP connection's sender state
// (diagnostics).
func (g *Gateway) TCPInfo() []string {
	var out []string
	g.eps.Range(func(k, _ any) bool {
		var ti tcpip.TCPInfoOption
		if err := k.(tcpip.Endpoint).GetSockOpt(&ti); err == nil {
			out = append(out, fmt.Sprintf("state=%v cc=%v cwnd=%d ssthresh=%d rtt=%v rttvar=%v rto=%v reorder=%v sndbuf=%d",
				ti.State, ti.CcState, ti.SndCwnd, ti.SndSsthresh, ti.RTT, ti.RTTVar, ti.RTO, ti.ReorderSeen, k.(tcpip.Endpoint).SocketOptions().GetSendBufferSize()))
		}
		return true
	})
	return out
}

// TCPStats exposes the userspace TCP stack's loss-recovery counters for the
// dashboard: they show whether inner connections are hitting timeouts or
// recovering cleanly.
func (g *Gateway) TCPStats() map[string]uint64 {
	st := g.stack.Stats().TCP
	return map[string]uint64{
		"segmentsSent":     st.SegmentsSent.Value(),
		"retransmits":      st.Retransmits.Value(),
		"fastRetransmit":   st.FastRetransmit.Value(),
		"fastRecovery":     st.FastRecovery.Value(),
		"sackRecovery":     st.SACKRecovery.Value(),
		"tlpRecovery":      st.TLPRecovery.Value(),
		"timeouts":         st.Timeouts.Value(),
		"slowStartRetrans": st.SlowStartRetransmits.Value(),
		"spuriousRecovery": st.SpuriousRecovery.Value(),
		"spuriousRTO":      st.SpuriousRTORecovery.Value(),
		"dsackAcked":       st.SegmentsAckedWithDSACK.Value(),
	}
}
