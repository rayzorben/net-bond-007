// Package server is the Net-Bond-007 aggregation endpoint. It accepts every link of
// every client on one UDP socket, de-duplicates upstream packets (first copy
// wins), forwards them to the internet through the userspace gateway and
// schedules responses back over the client's links per the client's policy.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rayzorben/net-bond-007/internal/bond"
	"github.com/rayzorben/net-bond-007/internal/config"
	"github.com/rayzorben/net-bond-007/internal/names"
	"github.com/rayzorben/net-bond-007/internal/netstack"
	"github.com/rayzorben/net-bond-007/internal/proto"
	"github.com/rayzorben/net-bond-007/internal/sched"
	"github.com/rayzorben/net-bond-007/internal/webui"
)

var Version = "dev"

type Server struct {
	cfg *config.Store[config.Server]
	log *slog.Logger

	lns    atomic.Pointer[[]*packetConn]
	connMu sync.Mutex
	listen string

	mu       sync.RWMutex
	sessions map[uint32]*Session
	byIP     map[netip.Addr]*Session
	pool     *ipPool

	gw      *netstack.Gateway
	gwAddr  netip.Addr
	started time.Time
	closed  chan struct{}

	Rejected  atomic.Uint64
	names     names.Cache
	hist      *Recorder
	lastSamp  time.Time
	lastPrune time.Time
}

// EnableHistory starts recording history under dir.
func (s *Server) EnableHistory(dir string) error {
	r, err := NewRecorder(dir)
	if err != nil {
		return err
	}
	s.hist = r
	return nil
}

func (s *Server) event(ss *Session, msg string) {
	if s.hist == nil || !s.cfg.Get().History.Enabled {
		return
	}
	s.hist.Write(Record{T: time.Now().UnixMilli(), Kind: "event", Client: ss.Name, Session: fmt.Sprintf("%08x", ss.ID), Event: msg})
}

// recordConns writes a summary of each ended connection (server view: what
// it sends is the client's download).
func (s *Server) recordConns(ss *Session, fs []bond.FlowView) {
	if s.hist == nil || !s.cfg.Get().History.Enabled {
		return
	}
	for _, f := range fs {
		secs := float64(f.Last-f.First) / 1000
		hc := &HistConn{Host: f.Name, Outside: f.Outside, Proto: f.Proto, DownBytes: f.TxBytes, UpBytes: f.RxBytes,
			Seconds: secs, PeakDown: f.PeakTx, Links: map[string]float64{}}
		if hc.Host == "" {
			hc.Host = s.names.Lookup(f.Key.Outside.Addr())
		}
		if secs > 0 {
			hc.AvgDown = float64(f.TxBytes) * 8 / secs
		}
		var total uint64
		for _, l := range f.Links {
			total += l.Copies
		}
		for _, l := range f.Links {
			if total > 0 {
				hc.Links[l.Name] = float64(l.Copies) / float64(total)
			}
		}
		s.hist.Write(Record{T: f.Last, Kind: "conn", Client: ss.Name, Session: fmt.Sprintf("%08x", ss.ID), Conn: hc})
	}
}

// sample records one history point per session.
func (s *Server) sample(now time.Time) {
	cfg := s.cfg.Get().History
	if s.hist == nil || !cfg.Enabled || now.Sub(s.lastSamp) < time.Duration(cfg.SampleSec)*time.Second {
		return
	}
	s.lastSamp = now
	s.mu.RLock()
	list := make([]*Session, 0, len(s.sessions))
	for _, ss := range s.sessions {
		list = append(list, ss)
	}
	s.mu.RUnlock()
	for _, ss := range list {
		ss.policyMu.Lock()
		mode := ss.policy.AutoMode
		ss.policyMu.Unlock()
		ev := ss.Engine.View()
		if mode == "" {
			mode = ev.Mode
		}
		hs := &HistSample{Mode: mode, Copies: ev.Redundancy, DownBps: ev.TxBps, UpBps: ev.RxBps, Retransmits: ev.Retransmits}
		for _, l := range ss.Engine.Links() {
			v := l.View(0)
			hs.Links = append(hs.Links, HistLink{Name: v.Name, Kind: v.Kind, State: v.State, RTT: v.RTTms, Loss: v.LossPct,
				DownBps: v.TxBps, UpBps: v.RxBps, Benched: v.Benched})
		}
		s.hist.Write(Record{T: now.UnixMilli(), Kind: "sample", Client: ss.Name, Session: fmt.Sprintf("%08x", ss.ID), Sample: hs})
	}
	if now.Sub(s.lastPrune) > time.Hour {
		s.hist.Prune(cfg.RetentionDays)
		s.lastPrune = now
	}
}

// Session is one client device and its bonded links.
type Session struct {
	ID       uint32
	Name     string
	OS       string
	Version  string
	IP       netip.Addr
	Created  time.Time
	lastSeen atomic.Int64
	Engine   *bond.Engine
	paths    sync.Map // link id -> *atomic.Pointer[path]
	policyMu sync.Mutex
	policy   proto.Policy
}

func New(cfg *config.Store[config.Server], log *slog.Logger) (*Server, error) {
	c := cfg.Get()
	pfx := netip.MustParsePrefix(c.Network.Pool)
	pool := newPool(pfx)
	s := &Server{
		cfg: cfg, log: log,
		sessions: map[uint32]*Session{},
		byIP:     map[netip.Addr]*Session{},
		pool:     pool,
		gwAddr:   pool.gateway,
		started:  time.Now(),
		closed:   make(chan struct{}),
	}
	gw, err := netstack.New(pool.gateway, c.Network.MTU, c.Egress.TCPBufferKB<<10, gatewayOpts(c), s.toClient, log)
	if err != nil {
		return nil, err
	}
	gw.MaxPer.Store(int64(c.Egress.MaxConnsPerUser))
	s.gw = gw
	if err := s.rebind(c.Listen); err != nil {
		return nil, err
	}
	cfg.Subscribe(s.onConfig)
	go s.janitor()
	return s, nil
}

func gatewayOpts(c *config.Server) netstack.Options {
	return netstack.Options{
		DNSUpstream:    c.Egress.DNSUpstream,
		AllowPrivate:   c.Egress.AllowPrivate,
		TCPDialTimeout: time.Duration(c.Egress.TCPDialTimeout) * time.Millisecond,
		UDPIdleTimeout: time.Duration(c.Egress.UDPIdleTimeout) * time.Second,
		TCPIdleTimeout: time.Duration(c.Egress.TCPIdleTimeout) * time.Second,
		ICMPEcho:       c.Egress.ICMPEcho,
	}
}

// Gateway exposes the userspace stack (tests replace its dialer).
func (s *Server) Gateway() *netstack.Gateway { return s.gw }

// Addr returns the bound UDP address.
func (s *Server) Addr() netip.AddrPort {
	ap := (*s.lns.Load())[0].c.LocalAddr().(*net.UDPAddr).AddrPort()
	if ap.Addr().IsUnspecified() {
		ap = netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), ap.Port())
	}
	return ap
}

func (s *Server) onConfig(old, c *config.Server) {
	s.gw.SetOptions(gatewayOpts(c))
	s.gw.MaxPer.Store(int64(c.Egress.MaxConnsPerUser))
	if c.Listen != old.Listen {
		if err := s.rebind(c.Listen); err != nil {
			s.log.Error("rebind failed", "listen", c.Listen, "err", err)
		}
	}
	if c.Network.Pool != old.Network.Pool || c.Network.MTU != old.Network.MTU || c.Egress.TCPBufferKB != old.Egress.TCPBufferKB {
		s.log.Warn("network pool/MTU/TCP buffer changes apply to the next server start (existing tunnels keep their addresses)")
	}
	s.mu.RLock()
	for _, ss := range s.sessions {
		ss.Engine.SetHistory(c.Sessions.HistorySeconds)
		s.applyPolicy(ss)
	}
	s.mu.RUnlock()
}

func (s *Server) rebind(addr string) error {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	lns, err := listenAll(addr)
	if err != nil {
		return err
	}
	old := s.lns.Swap(&lns)
	s.listen = addr
	for _, pc := range lns {
		go s.readLoop(pc)
		s.log.Info("bond listener ready", "udp", pc.c.LocalAddr().String())
	}
	if old != nil {
		for _, pc := range *old {
			pc.close()
		}
	}
	return nil
}

func (s *Server) Close() {
	defer s.hist.Close()
	close(s.closed)
	if lns := s.lns.Load(); lns != nil {
		for _, pc := range *lns {
			pc.close()
		}
	}
	s.mu.Lock()
	for _, ss := range s.sessions {
		ss.Engine.Close()
	}
	s.mu.Unlock()
	s.gw.Close()
}

func (s *Server) readLoop(pc *packetConn) {
	buf := make([]byte, proto.MaxFrame)
	for {
		n, from, local, err := pc.read(buf)
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.handle(buf[:n], newPathLazy(pc, from, local))
	}
}

// newPathLazy avoids allocating a path per packet: handlers only build one
// when the peer's address changed.
type pkt struct {
	pc    *packetConn
	from  netip.AddrPort
	local netip.Addr
}

func newPathLazy(pc *packetConn, from netip.AddrPort, local netip.Addr) pkt {
	return pkt{pc, from, local}
}

func (s *Server) handle(f []byte, from pkt) {
	h, err := proto.ParseHeader(f)
	if err != nil {
		return
	}
	if h.Type == proto.TypeHello {
		s.hello(f, h, from)
		return
	}
	s.mu.RLock()
	ss := s.sessions[h.Session]
	s.mu.RUnlock()
	if ss == nil {
		s.reject(h, from, "unknown-session")
		return
	}
	l := ss.Engine.Link(h.Link)
	if l == nil {
		s.reject(h, from, "unknown-link")
		return
	}
	now := time.Now()
	ss.lastSeen.Store(now.UnixNano())
	l.LastRx.Store(now.UnixNano())
	ss.updateAddr(l, from)

	switch h.Type {
	case proto.TypeData:
		ss.Engine.Receive(l, f)
	case proto.TypeProbe:
		p, err := proto.ParseProbe(f)
		if err != nil {
			return
		}
		l.Rx.Add(len(f))
		before := l.State()
		s.applyProbe(l, p)
		if after := l.State(); after != before && before != bond.StateConnecting {
			s.event(ss, fmt.Sprintf("link %s %s → %s", l.Name, before, after))
		}
		ack := proto.AppendProbeAck(nil, ss.ID, l.ID, proto.ProbeAck{
			Seq: p.Seq, SentAt: p.SentAt,
			RxPackets: l.Rx.Packets.Load(), RxBytes: l.Rx.Bytes.Load(),
		})
		l.SendRaw(ack)
	case proto.TypePolicy:
		var p proto.Policy
		if proto.ParseJSON(f, &p) == nil {
			ss.policyMu.Lock()
			old := ss.policy
			ss.policy = p
			ss.policyMu.Unlock()
			s.applyPolicy(ss)
			if p.AutoMode != old.AutoMode || p.AutoDirectVia != old.AutoDirectVia || fmt.Sprint(p.Benched) != fmt.Sprint(old.Benched) {
				s.event(ss, autoEvent(ss, p))
			}
		}
	case proto.TypeNack:
		l.Rx.Add(len(f))
		ss.Engine.HandleNack(f)
	case proto.TypeBye:
		ss.Engine.RemoveLink(l.ID)
		ss.paths.Delete(l.ID)
	}
}

func (s *Server) reject(h proto.Header, to pkt, reason string) {
	s.Rejected.Add(1)
	f, _ := proto.AppendJSON(nil, proto.TypeReject, h.Session, h.Link, proto.Reject{Reason: reason})
	to.pc.write(f, newPath(to.pc, to.from, to.local))
}

func (s *Server) hello(f []byte, h proto.Header, from pkt) {
	var hello proto.Hello
	if err := proto.ParseJSON(f, &hello); err != nil {
		return
	}
	cfg := s.cfg.Get()
	if cfg.Token != "" && subtle.ConstantTimeCompare([]byte(hello.Token), []byte(cfg.Token)) != 1 {
		s.log.Warn("rejected client: bad token", "from", from.from, "client", hello.ClientName)
		s.reject(h, from, "bad-token")
		return
	}
	if h.Session == 0 {
		return
	}
	s.mu.Lock()
	ss := s.sessions[h.Session]
	if ss == nil {
		if len(s.sessions) >= cfg.Sessions.MaxSessions {
			s.mu.Unlock()
			s.reject(h, from, "server-full")
			return
		}
		ip, ok := s.pool.alloc(hello.ClientName)
		if !ok {
			s.mu.Unlock()
			s.reject(h, from, "address-pool-exhausted")
			return
		}
		ss = &Session{
			ID: h.Session, Name: hello.ClientName, OS: hello.OS, Version: hello.Version,
			IP: ip, Created: time.Now(),
		}
		ss.Engine = bond.New(h.Session, s.fromClient(ss))
		ss.Engine.SetHistory(cfg.Sessions.HistorySeconds)
		sess := ss
		ss.Engine.SetFlowSink(func(fs []bond.FlowView) { s.recordConns(sess, fs) })
		ss.Engine.SetQueueDepth(cfg.Sessions.DefaultQueue)
		s.sessions[ss.ID] = ss
		s.byIP[ip] = ss
		s.log.Info("client connected", "client", hello.ClientName, "session", fmt.Sprintf("%08x", ss.ID), "ip", ip, "os", hello.OS)
		defer s.event(ss, fmt.Sprintf("client connected (v%s, %s, tunnel %s)", hello.Version, hello.OS, ip))
	}
	s.mu.Unlock()
	ss.lastSeen.Store(time.Now().UnixNano())

	l := ss.Engine.Link(h.Link)
	if l == nil || l.Name != hello.LinkName {
		ap := &atomic.Pointer[path]{}
		ap.Store(newPath(from.pc, from.from, from.local))
		ss.paths.Store(h.Link, ap)
		l = ss.Engine.AddLink(h.Link, hello.LinkName, hello.LinkKind, linkSender(ap))
		l.SetState(bond.StateConnecting)
		l.Health(func(hh *bond.Health) { hh.RemoteAddr = from.from.String() })
		s.log.Info("link joined", "client", ss.Name, "link", hello.LinkName, "kind", hello.LinkKind, "from", from.from)
		s.event(ss, fmt.Sprintf("link %s (%s) joined from %s", hello.LinkName, hello.LinkKind, from.from))
	}
	l.LastRx.Store(time.Now().UnixNano())
	ss.updateAddr(l, from)
	ack, _ := proto.AppendJSON(nil, proto.TypeHelloAck, ss.ID, h.Link, proto.HelloAck{
		ClientIP:   ss.IP.String(),
		PrefixLen:  s.pool.prefix.Bits(),
		GatewayIP:  s.gwAddr.String(),
		DNS:        s.gwAddr.String(),
		MTU:        cfg.Network.MTU,
		ServerName: cfg.Name,
		Version:    Version,
		Epoch:      ss.Created.UnixNano(),
	})
	l.SendRaw(ack)
	s.applyPolicy(ss)
}

func (ss *Session) updateAddr(l *bond.Link, from pkt) {
	v, ok := ss.paths.Load(l.ID)
	if !ok {
		return
	}
	ap := v.(*atomic.Pointer[path])
	if cur := ap.Load(); cur == nil || cur.remote != from.from || cur.local != from.local || cur.pc != from.pc {
		ap.Store(newPath(from.pc, from.from, from.local))
		l.Health(func(h *bond.Health) { h.RemoteAddr = from.from.String() })
	}
}

func linkSender(ap *atomic.Pointer[path]) func([]byte) error {
	return func(f []byte) error {
		p := ap.Load()
		if p == nil {
			return net.ErrClosed
		}
		return p.pc.write(f, p)
	}
}

// fromClient receives de-duplicated upstream packets for a session.
func (s *Server) fromClient(ss *Session) func([]byte) {
	return func(pkt []byte) {
		// Anti-spoofing: a client may only send from its assigned address.
		if len(pkt) >= 20 && pkt[0]>>4 == 4 {
			if netip.AddrFrom4([4]byte(pkt[12:16])) != ss.IP {
				return
			}
		} else {
			return // IPv6 inside the tunnel is not supported yet
		}
		s.gw.Inject(pkt)
	}
}

// toClient sends a downstream packet from the gateway to the owning client.
func (s *Server) toClient(dst netip.Addr, pkt []byte) {
	s.mu.RLock()
	ss := s.byIP[dst]
	s.mu.RUnlock()
	if ss != nil {
		ss.Engine.Send(pkt)
	}
}

func (s *Server) applyProbe(l *bond.Link, p proto.Probe) {
	l.SetState(bond.State(p.State & proto.ProbeStateMask))
	l.SetBackup(p.State&proto.ProbeBackupBit != 0)
	var srtt, minRTT time.Duration
	var loss float64
	last := time.Duration(p.LastRTTMicr) * time.Microsecond
	l.Health(func(h *bond.Health) {
		rtt := time.Duration(p.RTTMicros) * time.Microsecond
		if rtt > 0 {
			h.RTT.SRTT = rtt
			h.RTT.RTTVar = time.Duration(p.JitterMicro) * time.Microsecond
			if h.RTT.Min == 0 || rtt < h.RTT.Min {
				h.RTT.Min = rtt
			}
			h.RTT.Samples++
		}
		h.Loss = float64(p.LossPermil) / 1000
		h.ManualW = float64(p.Weight) / 10
		if s.cfg.Get().Sessions.TrustClientRate {
			h.CapBps = float64(p.CapKbps) * 1000
		}
		if last > 0 && last < h.RTT.Min {
			h.RTT.Min = last
		}
		srtt, minRTT, loss = h.RTT.SRTT, h.RTT.Min, h.Loss
	})
	recent := srtt
	if last > 0 {
		recent = (srtt + last) / 2 // react faster than the smoothed value
	}
	l.Pacer.Govern(time.Now(), recent, minRTT, loss, float64(p.RxKbps)*1000)
	l.Shaper.Observe(time.Now(), float64(p.RxKbps)*1000)
	l.SetOneWayDelay(srtt / 2)
}

func (s *Server) applyPolicy(ss *Session) {
	cfg := s.cfg.Get()
	ss.policyMu.Lock()
	p := ss.policy
	ss.policyMu.Unlock()
	ports, _ := sched.ParsePorts(p.RealtimePorts)
	mode := sched.Mode(p.Mode)
	if mode == "" {
		mode = sched.ModeSmart
	}
	if cfg.Sessions.ForceMirror {
		mode = sched.ModeMirror
	}
	for _, l := range ss.Engine.Links() {
		l.Pacer.Configure(p.Pacing, float64(max(p.PacingStartMbps, 1))*1e6, p.LinkMaxMbps[l.ID]*1e6)
		l.Pacer.Tuning(time.Duration(p.PacingDelayMs)*time.Millisecond, float64(p.PacingLossPct)/100)
		l.Shaper.Configure(p.Shaping)
	}
	ss.Engine.SetARQ(p.Retransmit)
	red := max(p.Redundancy, 1)
	if p.AutoCopies > 0 && mode == sched.ModeSmart {
		// The client's automatic scaling decided the copies.
		mode, red = sched.ModeCustom, p.AutoCopies
	}
	benched := map[uint8]bool{}
	for _, id := range p.Benched {
		benched[id] = true
	}
	for _, l := range ss.Engine.Links() {
		l.SetBenched(benched[l.ID])
		share, metered := p.MeteredShare[l.ID]
		l.SetMetered(metered, share)
	}
	ss.Engine.SetPolicy(sched.Policy{
		Mode: mode, Strategy: sched.Strategy(p.Strategy), Redundancy: red,
		SmallPacketMirrorBytes: p.SmallPacketMirrorBytes,
		RealtimeMirror:         p.RealtimeMirror,
		RealtimePorts:          ports,
	})
}

// updateWeights sets downstream scheduling weights: the user's manual weight
// if any, otherwise the pacer's learned rate (or the client-reported
// capacity), discounted for latency.
func (ss *Session) updateWeights() {
	ss.policyMu.Lock()
	latW := ss.policy.LatencyWeighting
	ss.policyMu.Unlock()
	links := ss.Engine.Links()
	var best time.Duration
	for _, l := range links {
		l.Health(func(h *bond.Health) {
			if h.RTT.SRTT > 0 && (best == 0 || h.RTT.SRTT < best) {
				best = h.RTT.SRTT
			}
		})
	}
	for _, l := range links {
		var w, capBps float64
		var rtt time.Duration
		l.Health(func(h *bond.Health) { w, capBps, rtt = h.ManualW, h.CapBps, h.RTT.SRTT })
		if w <= 0 {
			if r := l.Pacer.Rate(); r > 0 {
				capBps = r
			}
			w = bond.AutoWeight(capBps, rtt, best, latW)
		}
		l.SetWeight(w)
	}
}

func (ss *Session) reorderHold() time.Duration {
	ss.policyMu.Lock()
	p := ss.policy
	ss.policyMu.Unlock()
	switch p.ReorderMode {
	case "off":
		return 0
	case "fixed":
		return time.Duration(p.ReorderMs) * time.Millisecond
	default:
		maxHold := time.Duration(p.ReorderMs) * time.Millisecond
		if maxHold == 0 {
			maxHold = 60 * time.Millisecond
		}
		hold := ss.Engine.AutoReorderHold(maxHold)
		if ss.Engine.ARQ() {
			hold = ss.Engine.ARQHold(maxHold)
		}
		return hold
	}
}

// janitor runs once a second: rates, history, link timeouts, idle sessions.
func (s *Server) janitor() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.closed:
			return
		case now := <-t.C:
			s.tick(now)
		}
	}
}

func (s *Server) tick(now time.Time) {
	cfg := s.cfg.Get()
	linkTimeout := time.Duration(cfg.Sessions.LinkTimeoutMs) * time.Millisecond
	idle := time.Duration(cfg.Sessions.IdleTimeoutSec) * time.Second
	var expired []*Session
	s.mu.RLock()
	for _, ss := range s.sessions {
		if now.Sub(time.Unix(0, ss.lastSeen.Load())) > idle {
			expired = append(expired, ss)
			continue
		}
		for _, l := range ss.Engine.Links() {
			if now.Sub(time.Unix(0, l.LastRx.Load())) > linkTimeout && l.State() != bond.StateDisabled {
				l.SetState(bond.StateDown)
			}
		}
		ss.Engine.SetReorderHold(ss.reorderHold())
		ss.updateWeights()
		ss.Engine.Tick(now)
	}
	s.mu.RUnlock()
	s.sample(now)
	for _, ss := range expired {
		s.mu.Lock()
		delete(s.sessions, ss.ID)
		delete(s.byIP, ss.IP)
		s.pool.free(ss.IP)
		s.mu.Unlock()
		ss.Engine.Close()
		s.log.Info("client session expired", "client", ss.Name, "session", fmt.Sprintf("%08x", ss.ID))
		s.event(ss, "client session ended (idle)")
	}
}

// ---------------------------------------------------------------- dashboard

type SessionView struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	OS       string          `json:"os"`
	Version  string          `json:"version"`
	IP       string          `json:"ip"`
	Created  int64           `json:"created"`
	LastSeen int64           `json:"lastSeen"`
	Engine   bond.EngineView `json:"engine"`
	Links    []bond.LinkView `json:"links"`
}

type StateView struct {
	Role     string            `json:"role"`
	Uptime   float64           `json:"uptime"`
	Listen   string            `json:"listen"`
	Gateway  string            `json:"gateway"`
	Sessions []SessionView     `json:"sessions"`
	Egress   map[string]any    `json:"egress"`
	TCP      map[string]uint64 `json:"tcp"`
	Rejected uint64            `json:"rejected"`
	Warnings []string          `json:"warnings"`
}

func (s *Server) Snapshot(history int) StateView {
	cfg := s.cfg.Get()
	st := &s.gw.Stats
	v := StateView{
		Role:    "server",
		Uptime:  time.Since(s.started).Seconds(),
		Listen:  s.listen,
		Gateway: s.gwAddr.String(),
		Egress: map[string]any{
			"tcpActive": st.TCPActive.Load(), "tcpTotal": st.TCPTotal.Load(), "tcpFailed": st.TCPFailed.Load(),
			"udpActive": st.UDPActive.Load(), "udpTotal": st.UDPTotal.Load(),
			"blocked": st.Blocked.Load(), "pings": st.Pings.Load(),
			"bytesUp": st.BytesUp.Load(), "bytesDown": st.BytesDown.Load(),
		},
		Rejected: s.Rejected.Load(),
		TCP:      s.gw.TCPStats(),
		Sessions: []SessionView{},
		Warnings: []string{},
	}
	if cfg.Token == "" {
		v.Warnings = append(v.Warnings, "No access token set: anyone who can reach the UDP port can use this server.")
	}
	if cfg.UI.Password == "" {
		v.Warnings = append(v.Warnings, "No dashboard password set.")
	}
	v.Warnings = append(v.Warnings, "Traffic between client and server is not encrypted (TLS inside the tunnel still is).")
	s.mu.RLock()
	for _, ss := range s.sessions {
		sv := SessionView{
			ID: fmt.Sprintf("%08x", ss.ID), Name: ss.Name, OS: ss.OS, Version: ss.Version,
			IP: ss.IP.String(), Created: ss.Created.UnixMilli(),
			LastSeen: time.Unix(0, ss.lastSeen.Load()).UnixMilli(),
			Engine:   ss.Engine.View(),
		}
		for _, l := range ss.Engine.Links() {
			sv.Links = append(sv.Links, l.View(history))
		}
		v.Sessions = append(v.Sessions, sv)
	}
	s.mu.RUnlock()
	sort.Slice(v.Sessions, func(i, j int) bool { return v.Sessions[i].Created < v.Sessions[j].Created })
	return v
}

// Backend adapts the server to the dashboard.
type Backend struct{ S *Server }

func (b Backend) Info() any {
	return map[string]any{"role": "server", "version": Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "name": b.S.cfg.Get().Name}
}
func (b Backend) State(h int) any { return b.S.Snapshot(h) }
func (b Backend) Config() any     { return b.S.cfg.Get() }
func (b Backend) Schema() any     { return config.Schema(b.S.cfg.Get()) }
func (b Backend) UpdateConfig(raw []byte) error {
	c := b.S.cfg.Clone()
	if err := json.Unmarshal(raw, c); err != nil {
		return err
	}
	return b.S.cfg.Update(c)
}
func (b Backend) Password() string { return b.S.cfg.Get().UI.Password }
func (b Backend) RefreshInterval() time.Duration {
	return time.Duration(b.S.cfg.Get().UI.RefreshMs) * time.Millisecond
}
func (b Backend) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/x/history", func(w http.ResponseWriter, r *http.Request) {
		hours, _ := strconv.ParseFloat(r.URL.Query().Get("hours"), 64)
		if hours <= 0 {
			hours = 6
		}
		to := time.Now()
		from := to.Add(-time.Duration(hours * float64(time.Hour)))
		samples, events, conns := b.S.hist.Query(from, to, r.URL.Query().Get("client"), 600)
		if len(conns) > 2000 {
			conns = conns[:2000]
		}
		if len(events) > 1000 {
			events = events[:1000]
		}
		clients := map[string]bool{}
		for _, x := range samples {
			clients[x.Client] = true
		}
		names := []string{}
		for c := range clients {
			names = append(names, c)
		}
		sort.Strings(names)
		webui.WriteJSON(w, http.StatusOK, map[string]any{
			"enabled": b.S.hist != nil && b.S.cfg.Get().History.Enabled, "from": from.UnixMilli(), "to": to.UnixMilli(),
			"clients": names, "samples": nonNil(samples), "events": nonNil(events), "connections": nonNil(conns),
		})
	})
	mux.HandleFunc("GET /api/x/connections", func(w http.ResponseWriter, r *http.Request) {
		type conn struct {
			bond.FlowView
			Host string `json:"host,omitempty"`
		}
		type sess struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			IP          string `json:"ip"`
			Connections []conn `json:"connections"`
		}
		out := []sess{}
		b.S.mu.RLock()
		list := make([]*Session, 0, len(b.S.sessions))
		for _, ss := range b.S.sessions {
			list = append(list, ss)
		}
		b.S.mu.RUnlock()
		for _, ss := range list {
			sv := sess{ID: fmt.Sprintf("%08x", ss.ID), Name: ss.Name, IP: ss.IP.String(), Connections: []conn{}}
			for _, f := range ss.Engine.Flows() {
				c := conn{FlowView: f, Host: f.Name}
				if c.Host == "" {
					c.Host = b.S.names.Lookup(f.Key.Outside.Addr())
				}
				sv.Connections = append(sv.Connections, c)
			}
			out = append(out, sv)
		}
		webui.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
	})
	mux.HandleFunc("POST /api/x/sessions/{id}/kick", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		b.S.mu.Lock()
		for _, ss := range b.S.sessions {
			if fmt.Sprintf("%08x", ss.ID) == id {
				delete(b.S.sessions, ss.ID)
				delete(b.S.byIP, ss.IP)
				b.S.pool.free(ss.IP)
				ss.Engine.Close()
			}
		}
		b.S.mu.Unlock()
		webui.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}

func nonNil(r []Record) []Record {
	if r == nil {
		return []Record{}
	}
	return r
}

func autoEvent(ss *Session, p proto.Policy) string {
	if p.AutoMode == "" {
		return "automatic scaling off (mode " + p.Mode + ")"
	}
	msg := "automatic scaling: " + p.AutoMode
	if p.AutoDirectVia != "" {
		msg += " via " + p.AutoDirectVia
	}
	msg += fmt.Sprintf(" (%d copies", p.AutoCopies)
	if len(p.Benched) > 0 {
		var ns []string
		for _, id := range p.Benched {
			if l := ss.Engine.Link(id); l != nil {
				ns = append(ns, l.Name)
			} else {
				ns = append(ns, fmt.Sprint(id))
			}
		}
		msg += ", benched: " + strings.Join(ns, ", ")
	}
	return msg + ")"
}
