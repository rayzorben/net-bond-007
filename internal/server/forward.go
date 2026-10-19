package server

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"github.com/rayzorben/net-bond-007/internal/proto"
	"github.com/rayzorben/net-bond-007/internal/sched"
)

// Port forwarding. A client asks (in its POLICY) for public ports; the
// server listens on them and relays every inbound connection into the
// tunnel to the client's address on the same port, with the real remote
// address as the source, like a home router's port forward. The client (in
// router mode) forwards it on to a LAN device.
//
// A forwarded UDP port is full-cone: the client's own outbound packets from
// that port leave from the same public socket, so peer-to-peer protocols
// (BitTorrent DHT and uTP) see one stable public address.

type fwdKey struct {
	proto string // "tcp" or "udp"
	port  uint16
}

type fwdPort struct {
	key   fwdKey
	owner *Session // guarded by forwarder.mu
	tcp   net.Listener
	udp   *net.UDPConn

	flowMu sync.Mutex
	flows  map[netip.AddrPort]*fwdFlow // UDP: remote peer -> flow into the tunnel
}

type fwdFlow struct {
	conn net.Conn // tunnel side (gVisor)
	last time.Time
}

// maxFwdFlows bounds the UDP peers one forwarded port tracks (a DHT node
// talks to a few thousand).
const maxFwdFlows = 8192

type forwarder struct {
	s     *Server
	mu    sync.Mutex
	ports map[fwdKey]*fwdPort
	// listen opens public sockets; tests bind to loopback.
	host string
}

func newForwarder(s *Server) *forwarder {
	f := &forwarder{s: s, ports: map[fwdKey]*fwdPort{}}
	s.gw.SetUDPClaim(f.claimUDP)
	return f
}

func expand(fw proto.Forward) []string {
	switch fw.Proto {
	case "tcp", "udp":
		return []string{fw.Proto}
	case "both", "":
		return []string{"tcp", "udp"}
	}
	return nil
}

// reconcile opens what ss asks for and closes what it no longer wants.
func (f *forwarder) reconcile(ss *Session, want []proto.Forward) proto.ForwardStatus {
	cfg := f.s.cfg.Get().Forward
	allowed, _ := sched.ParsePorts(cfg.Ports)
	st := proto.ForwardStatus{Enabled: !cfg.Disabled, Ports: cfg.Ports, Forwards: []proto.ForwardState{}}

	f.mu.Lock()
	defer f.mu.Unlock()
	keep := map[fwdKey]bool{}
	for _, fw := range want {
		fs := proto.ForwardState{Proto: fw.Proto, Port: fw.Port, State: "open"}
		protos := expand(fw)
		switch {
		case protos == nil:
			fs.State, fs.Error = "error", "unknown protocol "+fw.Proto
		case cfg.Disabled:
			fs.State = "off"
		case fw.Port == 0 || !allowed.Has(fw.Port):
			fs.State = "outside"
		}
		if fs.State != "open" {
			st.Forwards = append(st.Forwards, fs)
			continue
		}
		for _, p := range protos {
			k := fwdKey{p, fw.Port}
			fp := f.ports[k]
			if fp != nil && fp.owner != ss {
				if f.stale(fp.owner, ss) {
					f.s.log.Info("port forward moves to the client's new session", "port", k.port, "proto", k.proto, "client", ss.Name)
					fp.owner = ss
				} else {
					fs.State, fs.Error = "taken", fmt.Sprintf("%s %d is forwarded to %s", p, fw.Port, fp.owner.Name)
					continue
				}
			}
			if fp == nil {
				var err error
				if fp, err = f.open(k, ss); err != nil {
					fs.State, fs.Error = "error", err.Error()
					continue
				}
				f.ports[k] = fp
				f.s.log.Info("port forward opened", "port", k.port, "proto", k.proto, "client", ss.Name, "to", ss.IP)
				f.s.event(ss, fmt.Sprintf("port %d/%s forwarded", k.port, k.proto))
			}
			keep[k] = true
		}
		st.Forwards = append(st.Forwards, fs)
	}
	for k, fp := range f.ports {
		if fp.owner == ss && !keep[k] {
			f.closeLocked(fp, "no longer requested")
		}
	}
	return st
}

// stale: old belongs to a session the same client has replaced (it
// restarted, or the server forgot it), or to one that is gone.
func (f *forwarder) stale(old, cur *Session) bool {
	f.s.mu.RLock()
	alive := f.s.sessions[old.ID] == old
	f.s.mu.RUnlock()
	return !alive || old.Name == cur.Name
}

// release closes every port ss holds (its session ended).
func (f *forwarder) release(ss *Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fp := range f.ports {
		if fp.owner == ss {
			f.closeLocked(fp, "client gone")
		}
	}
}

// prune closes ports the configuration no longer allows.
func (f *forwarder) prune() {
	cfg := f.s.cfg.Get().Forward
	allowed, _ := sched.ParsePorts(cfg.Ports)
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, fp := range f.ports {
		if cfg.Disabled || !allowed.Has(k.port) {
			f.closeLocked(fp, "not allowed by the server's settings")
		}
	}
}

func (f *forwarder) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fp := range f.ports {
		f.closeLocked(fp, "server stopping")
	}
}

func (f *forwarder) closeLocked(fp *fwdPort, why string) {
	delete(f.ports, fp.key)
	if fp.tcp != nil {
		fp.tcp.Close()
	}
	if fp.udp != nil {
		fp.udp.Close()
	}
	fp.flowMu.Lock()
	for peer, fl := range fp.flows {
		fl.conn.Close()
		delete(fp.flows, peer)
	}
	fp.flowMu.Unlock()
	f.s.log.Info("port forward closed", "port", fp.key.port, "proto", fp.key.proto, "client", fp.owner.Name, "why", why)
}

// views lists the forwards of one session, for the dashboard.
func (f *forwarder) views(ss *Session) []proto.ForwardState {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []proto.ForwardState
	for k, fp := range f.ports {
		if fp.owner == ss {
			out = append(out, proto.ForwardState{Proto: k.proto, Port: k.port, State: "open"})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	return out
}

func (f *forwarder) open(k fwdKey, ss *Session) (*fwdPort, error) {
	fp := &fwdPort{key: k, owner: ss}
	addr := net.JoinHostPort(f.host, fmt.Sprint(k.port))
	switch k.proto {
	case "tcp":
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		fp.tcp = ln
		go f.acceptTCP(fp)
	case "udp":
		ua, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		pc, err := net.ListenUDP("udp", ua)
		if err != nil {
			return nil, err
		}
		fp.udp = pc
		fp.flows = map[netip.AddrPort]*fwdFlow{}
		go f.readUDP(fp)
	}
	return fp, nil
}

func (f *forwarder) owner(fp *fwdPort) *Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fp.owner
}

func (f *forwarder) acceptTCP(fp *fwdPort) {
	for {
		c, err := fp.tcp.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				f.s.log.Warn("port forward accept failed", "port", fp.key.port, "err", err)
			}
			return
		}
		peer := c.RemoteAddr().(*net.TCPAddr).AddrPort()
		peer = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())
		to := netip.AddrPortFrom(f.owner(fp).IP, fp.key.port)
		go func() {
			if err := f.s.gw.InboundTCP(c, peer, to); err != nil {
				f.s.log.Debug("port forward: client did not accept", "port", fp.key.port, "peer", peer, "err", err)
			}
		}()
	}
}

func (f *forwarder) readUDP(fp *fwdPort) {
	buf := make([]byte, 65535)
	for {
		n, peer, err := fp.udp.ReadFromUDPAddrPort(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				f.s.log.Warn("port forward read failed", "port", fp.key.port, "err", err)
			}
			return
		}
		peer = netip.AddrPortFrom(peer.Addr().Unmap(), peer.Port())
		fl := f.flow(fp, peer, nil)
		if fl == nil {
			continue
		}
		fl.conn.Write(buf[:n])
	}
}

// flow finds or creates the tunnel-side flow for peer. local is the
// client's own flow when the client spoke first (claimUDP); otherwise one
// is opened toward the client.
func (f *forwarder) flow(fp *fwdPort, peer netip.AddrPort, local net.Conn) *fwdFlow {
	to := netip.AddrPortFrom(f.owner(fp).IP, fp.key.port) // before flowMu: closeLocked nests them the other way
	fp.flowMu.Lock()
	defer fp.flowMu.Unlock()
	if fl := fp.flows[peer]; fl != nil {
		if local != nil { // the old flow's endpoint is gone; replace it
			fl.conn.Close()
			fl.conn = local
			go f.pumpUDP(fp, peer, fl)
		}
		fl.last = time.Now()
		return fl
	}
	if fp.flows == nil || len(fp.flows) >= maxFwdFlows {
		if local != nil {
			local.Close()
		}
		return nil
	}
	if local == nil {
		var err error
		local, err = f.s.gw.DialUDPFrom(peer, to)
		if err != nil {
			f.s.log.Debug("port forward: cannot open flow", "port", fp.key.port, "peer", peer, "err", err)
			return nil
		}
	}
	fl := &fwdFlow{conn: local, last: time.Now()}
	fp.flows[peer] = fl
	go f.pumpUDP(fp, peer, fl)
	return fl
}

// pumpUDP copies the client's packets to peer from the public socket until
// the flow idles out.
func (f *forwarder) pumpUDP(fp *fwdPort, peer netip.AddrPort, fl *fwdFlow) {
	conn := fl.conn
	defer func() {
		fp.flowMu.Lock()
		if fp.flows[peer] == fl && fl.conn == conn {
			delete(fp.flows, peer)
		}
		fp.flowMu.Unlock()
		conn.Close()
	}()
	idle := f.s.gw.UDPIdle()
	buf := make([]byte, 65535)
	for {
		conn.SetReadDeadline(time.Now().Add(idle))
		n, err := conn.Read(buf)
		if err != nil {
			fp.flowMu.Lock()
			recent := time.Since(fl.last) < idle && fl.conn == conn
			fp.flowMu.Unlock()
			var ne net.Error
			if recent && errors.As(err, &ne) && ne.Timeout() {
				continue // inbound traffic keeps the flow alive
			}
			return
		}
		fp.flowMu.Lock()
		fl.last = time.Now()
		fp.flowMu.Unlock()
		if _, err := fp.udp.WriteToUDPAddrPort(buf[:n], peer); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
	}
}

// claimUDP takes the client's outbound flows from a forwarded UDP port, so
// they leave from the public socket instead of a fresh one.
func (f *forwarder) claimUDP(src, dst netip.AddrPort) func(net.Conn) {
	f.mu.Lock()
	fp := f.ports[fwdKey{"udp", src.Port()}]
	if fp == nil || fp.owner.IP != src.Addr() {
		fp = nil
	}
	f.mu.Unlock()
	if fp == nil {
		return nil
	}
	return func(local net.Conn) { f.flow(fp, dst, local) }
}
