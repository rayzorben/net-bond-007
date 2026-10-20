package netstack

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// UDP NAT behaviour. Each inner source (client tunnel ip:port) gets one
// public UDP socket, shared by all of its destinations: endpoint-independent
// mapping, the "easy NAT" of RFC 4787. Dialing a fresh socket per flow gave
// every destination a different public port (a symmetric NAT), which
// defeats peer-to-peer hole punching: Tailscale, WebRTC, game consoles and
// the like fall back to relays. Filtering stays address-and-port dependent:
// a packet reaches the client only from a remote it has sent to.

type udpMapping struct {
	conn  *net.UDPConn
	mu    sync.Mutex
	flows map[netip.AddrPort]*udpFlow // remote -> inner flow
}

type udpFlow struct {
	local net.Conn     // client side (gVisor)
	last  atomic.Int64 // unix nanos of the last packet either way
}

func (f *udpFlow) touch() { f.last.Store(time.Now().UnixNano()) }

// natFlow relays one inner UDP flow (src -> dst) through src's shared
// socket until it has been idle in both directions for idle.
func (g *Gateway) natFlow(src, dst netip.AddrPort, local net.Conn, idle time.Duration) {
	dst = netip.AddrPortFrom(dst.Addr().Unmap(), dst.Port())
	f := &udpFlow{local: local}
	f.touch()
	m, err := g.natAdd(src, dst, f)
	if err != nil {
		g.log.Debug("udp nat socket failed", "src", src, "err", err)
		return
	}
	defer g.natRemove(src, dst, m, f)
	g.Stats.UDPTotal.Add(1)
	g.Stats.UDPActive.Add(1)
	defer g.Stats.UDPActive.Add(-1)
	buf := make([]byte, 65535)
	for {
		local.SetReadDeadline(time.Now().Add(idle))
		n, err := local.Read(buf)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) && time.Since(time.Unix(0, f.last.Load())) < idle {
				continue // replies are still arriving
			}
			return
		}
		f.touch()
		g.Stats.BytesUp.Add(int64(n))
		if _, err := m.conn.WriteToUDPAddrPort(buf[:n], dst); err != nil {
			g.log.Debug("udp nat write failed", "dst", dst, "err", err)
		}
	}
}

func (g *Gateway) natAdd(src, dst netip.AddrPort, f *udpFlow) (*udpMapping, error) {
	g.natMu.Lock()
	defer g.natMu.Unlock()
	m := g.nat[src]
	if m == nil {
		conn, err := net.ListenUDP("udp", nil)
		if err != nil {
			return nil, err
		}
		m = &udpMapping{conn: conn, flows: map[netip.AddrPort]*udpFlow{}}
		g.nat[src] = m
		go g.natRead(m)
	}
	m.mu.Lock()
	if old := m.flows[dst]; old != nil {
		old.local.Close() // a stale duplicate; the new flow replaces it
	}
	m.flows[dst] = f
	m.mu.Unlock()
	return m, nil
}

// natRemove drops f and closes the socket once src has no flows left.
func (g *Gateway) natRemove(src, dst netip.AddrPort, m *udpMapping, f *udpFlow) {
	g.natMu.Lock()
	defer g.natMu.Unlock()
	m.mu.Lock()
	if m.flows[dst] == f {
		delete(m.flows, dst)
	}
	empty := len(m.flows) == 0
	m.mu.Unlock()
	if empty && g.nat[src] == m {
		delete(g.nat, src)
		m.conn.Close()
	}
}

// natRead delivers packets from the internet to the flow they answer and
// drops the rest.
func (g *Gateway) natRead(m *udpMapping) {
	buf := make([]byte, 65535)
	for {
		n, from, err := m.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		m.mu.Lock()
		f := m.flows[from]
		m.mu.Unlock()
		if f == nil {
			continue
		}
		f.touch()
		g.Stats.BytesDown.Add(int64(n))
		f.local.Write(buf[:n])
	}
}
