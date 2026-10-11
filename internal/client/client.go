// Package client is the Bond007 endpoint that runs on laptops, desktops and
// routers. It discovers network interfaces, opens one UDP path per interface
// to the server (each socket pinned to its interface), keeps every path under
// constant health monitoring, and moves the OS's traffic between a virtual
// network adapter and the bond.
package client

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rayzorben/bond007/internal/bond"
	"github.com/rayzorben/bond007/internal/config"
	"github.com/rayzorben/bond007/internal/platform"
	"github.com/rayzorben/bond007/internal/proto"
	"github.com/rayzorben/bond007/internal/sched"
	"github.com/rayzorben/bond007/internal/webui"
)

var Version = "dev"

// PacketIO abstracts the virtual adapter so tests can run without root.
type PacketIO interface {
	Write(pkt []byte) error
}

type Client struct {
	cfg *config.Store[config.Client]
	log *slog.Logger

	// Overridable for tests.
	Discover func() ([]platform.Iface, error)
	Dial     func(iface platform.Iface, server netip.AddrPort) (*net.UDPConn, error)
	// NoTunnel disables the OS adapter; packets go to Deliver instead.
	NoTunnel bool
	Deliver  func(pkt []byte)

	engine atomic.Pointer[bond.Engine]

	mu        sync.Mutex
	links     map[string]*clink
	ids       map[string]uint8
	ifaces    []platform.Iface
	server    netip.AddrPort
	serverErr string
	epoch     int64
	// Re-resolve the server name when nothing has worked for a while: a
	// redeployed server (e.g. a recreated cloud container) may get a new IP.
	resolvedAt  time.Time
	lastHealthy time.Time

	ack     atomic.Pointer[proto.HelloAck]
	tunMu   sync.Mutex
	tun     *platform.Tun
	tunSpec platform.TunnelSpec
	tunUndo func()
	tunErr  string

	policySent atomic.Int64
	started    time.Time
	closed     chan struct{}
	closeOnce  sync.Once
	kick       chan struct{}
}

func New(cfg *config.Store[config.Client], log *slog.Logger) *Client {
	c := &Client{
		cfg: cfg, log: log,
		Discover: platform.Discover,
		links:    map[string]*clink{},
		ids:      map[string]uint8{},
		started:  time.Now(),
		closed:   make(chan struct{}),
		kick:     make(chan struct{}, 1),
	}
	c.Dial = c.dialBound
	return c
}

// Start begins discovery and connection. It returns immediately.
func (c *Client) Start() {
	c.newSession()
	c.cfg.Subscribe(c.onConfig)
	go c.loop()
	c.poke()
}

func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.mu.Lock()
		for name, l := range c.links {
			l.close(true)
			delete(c.links, name)
		}
		c.mu.Unlock()
		if e := c.engine.Load(); e != nil {
			e.Close()
		}
		c.closeTunnel()
	})
}

func (c *Client) poke() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func randomSession() uint32 {
	var b [4]byte
	for {
		rand.Read(b[:])
		if s := binary.BigEndian.Uint32(b[:]); s != 0 {
			return s
		}
	}
}

// newSession starts a fresh bond session (new id, new engine).
func (c *Client) newSession() {
	cfg := c.cfg.Get()
	e := bond.New(randomSession(), c.deliver)
	e.SetHistory(cfg.Health.HistorySeconds)
	e.SetQueueDepth(cfg.Bonding.QueueDepth)
	c.applyPolicy(e, cfg)
	if old := c.engine.Swap(e); old != nil {
		old.Close()
	}
	c.mu.Lock()
	c.epoch = 0
	c.mu.Unlock()
	c.ack.Store(nil)
	c.policySent.Store(0)
}

func (c *Client) Engine() *bond.Engine { return c.engine.Load() }

func (c *Client) deliver(pkt []byte) {
	if c.Deliver != nil {
		c.Deliver(pkt)
		return
	}
	c.tunMu.Lock()
	t := c.tun
	c.tunMu.Unlock()
	if t != nil {
		t.Write(pkt)
	}
}

// SendPacket pushes one outbound IP packet into the bond.
func (c *Client) SendPacket(pkt []byte) {
	if e := c.engine.Load(); e != nil {
		e.Send(pkt)
	}
}

func (c *Client) applyPolicy(e *bond.Engine, cfg *config.Client) {
	ports, _ := sched.ParsePorts(cfg.Bonding.RealtimePorts)
	e.SetPolicy(sched.Policy{
		Mode:                   sched.Mode(cfg.Bonding.Mode),
		Strategy:               sched.Strategy(cfg.Bonding.Strategy),
		Redundancy:             cfg.Bonding.Redundancy,
		SmallPacketMirrorBytes: cfg.Bonding.SmallPacketMirrorBytes,
		RealtimeMirror:         cfg.Bonding.RealtimeMirror,
		RealtimePorts:          ports,
	})
}

func (c *Client) wirePolicy() proto.Policy {
	cfg := c.cfg.Get()
	caps := map[uint8]float64{}
	c.mu.Lock()
	for name, l := range c.links {
		if m := cfg.Links[name].MaxMbps; m > 0 {
			caps[l.id] = m
		}
	}
	c.mu.Unlock()
	return proto.Policy{
		Pacing:                 cfg.Bonding.Pacing,
		PacingStartMbps:        cfg.Bonding.PacingStartMbps,
		LatencyWeighting:       cfg.Bonding.LatencyWeighting,
		LinkMaxMbps:            caps,
		Mode:                   cfg.Bonding.Mode,
		Strategy:               cfg.Bonding.Strategy,
		Redundancy:             cfg.Bonding.Redundancy,
		SmallPacketMirrorBytes: cfg.Bonding.SmallPacketMirrorBytes,
		RealtimeMirror:         cfg.Bonding.RealtimeMirror,
		RealtimePorts:          cfg.Bonding.RealtimePorts,
		ReorderMode:            cfg.Bonding.ReorderMode,
		ReorderMs:              cfg.Bonding.ReorderMs,
		QueueDepth:             cfg.Bonding.QueueDepth,
	}
}

func (c *Client) onConfig(old, cfg *config.Client) {
	e := c.engine.Load()
	if old.Server != cfg.Server {
		c.log.Info("server settings changed; reconnecting")
		c.mu.Lock()
		for name, l := range c.links {
			l.close(true)
			delete(c.links, name)
		}
		c.server = netip.AddrPort{}
		c.mu.Unlock()
		c.newSession()
		e = c.engine.Load()
		if !cfg.Server.Enabled {
			c.closeTunnel()
		}
	}
	c.applyPolicy(e, cfg)
	e.SetHistory(cfg.Health.HistorySeconds)
	c.policySent.Store(0) // resend policy to the server now
	c.mu.Lock()
	for name, l := range c.links {
		l.link.Pacer.Configure(cfg.Bonding.Pacing, float64(cfg.Bonding.PacingStartMbps)*1e6, cfg.Links[name].MaxMbps*1e6)
		l.link.Pacer.Tuning(time.Duration(cfg.Bonding.PacingDelayMs)*time.Millisecond, float64(cfg.Bonding.PacingLossPct)/100)
	}
	c.mu.Unlock()
	if old.Tunnel != cfg.Tunnel {
		go c.applyTunnel()
	}
	c.poke()
}

// loop drives discovery, reconciliation and the 1 Hz metrics tick.
func (c *Client) loop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastScan := time.Time{}
	for {
		select {
		case <-c.closed:
			return
		case <-c.kick:
			c.reconcile()
			lastScan = time.Now()
		case now := <-tick.C:
			if now.Sub(lastScan) >= time.Duration(c.cfg.Get().Interfaces.ScanMs)*time.Millisecond {
				c.reconcile()
				lastScan = now
			}
			c.tick(now)
		}
	}
}

func (c *Client) resolveServer() (netip.AddrPort, error) {
	addr := c.cfg.Get().Server.Address
	if addr == "" {
		return netip.AddrPort{}, errors.New("no server configured")
	}
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		return ap, nil
	}
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		ua, err = net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return netip.AddrPort{}, err
		}
	}
	return ua.AddrPort(), nil
}

// eligibility explains why an interface is or isn't bonded.
func (c *Client) eligibility(cfg *config.Client, it platform.Iface) string {
	lc, configured := cfg.Links[it.Name]
	tunName := c.tunName()
	switch {
	case tunName != "" && it.Name == tunName:
		return "tunnel"
	case config.MatchPattern(cfg.Interfaces.Exclude, it.Name):
		return "excluded"
	case lc.Disabled:
		return "disabled"
	case !it.Up:
		return "down"
	case len(it.IPv4) == 0:
		return "no-ipv4"
	case !cfg.Interfaces.AutoAdd && !configured:
		return "available"
	}
	return "bonded"
}

func (c *Client) tunName() string {
	c.tunMu.Lock()
	defer c.tunMu.Unlock()
	if c.tun != nil {
		return c.tun.Name()
	}
	return ""
}

func (c *Client) reconcile() {
	cfg := c.cfg.Get()
	ifs, err := c.Discover()
	if err != nil {
		c.log.Warn("interface discovery failed", "err", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ifaces = ifs

	if !cfg.Server.Enabled || cfg.Server.Address == "" {
		for name, l := range c.links {
			l.close(true)
			delete(c.links, name)
		}
		if cfg.Server.Address == "" {
			c.serverErr = "Set the server address to start bonding."
		} else {
			c.serverErr = "Bonding is switched off."
		}
		return
	}
	now := time.Now()
	for _, l := range c.links {
		if l.link.State().Usable() {
			c.lastHealthy = now
		}
	}
	if c.server.IsValid() && now.Sub(c.lastHealthy) > 20*time.Second && now.Sub(c.resolvedAt) > 20*time.Second {
		if ap, err := c.resolveServer(); err == nil && ap != c.server {
			c.log.Info("server address changed; reconnecting", "old", c.server, "new", ap)
			for name, l := range c.links {
				l.close(false)
				delete(c.links, name)
			}
			c.server = netip.AddrPort{}
		}
		c.resolvedAt = now
	}
	if !c.server.IsValid() {
		ap, err := c.resolveServer()
		if err != nil {
			c.serverErr = err.Error()
			return
		}
		c.server, c.serverErr = ap, ""
		c.resolvedAt, c.lastHealthy = now, now
	}

	want := map[string]platform.Iface{}
	for _, it := range ifs {
		if c.eligibility(cfg, it) == "bonded" {
			want[it.Name] = it
		}
	}
	for name, l := range c.links {
		it, ok := want[name]
		if !ok || it.PrimaryIPv4() != l.iface.PrimaryIPv4() || it.Index != l.iface.Index {
			c.log.Info("link removed", "link", name)
			l.close(true)
			delete(c.links, name)
		}
	}
	e := c.engine.Load()
	for name, it := range want {
		if _, ok := c.links[name]; ok {
			continue
		}
		id := c.linkID(name)
		l, err := c.openLink(e, it, id)
		if err != nil {
			c.log.Warn("cannot open link", "link", name, "err", err)
			continue
		}
		c.links[name] = l
		c.log.Info("link added", "link", name, "kind", it.Kind, "ip", it.PrimaryIPv4())
	}
	for name, l := range c.links {
		lc := cfg.Links[name]
		l.link.SetBackup(lc.Backup)
		l.link.Pacer.Configure(cfg.Bonding.Pacing, float64(cfg.Bonding.PacingStartMbps)*1e6, lc.MaxMbps*1e6)
		l.link.Pacer.Tuning(time.Duration(cfg.Bonding.PacingDelayMs)*time.Millisecond, float64(cfg.Bonding.PacingLossPct)/100)
		l.link.Health(func(h *bond.Health) {
			h.Label = lc.Label
			h.ManualCap = lc.MaxMbps * 1e6
			h.ManualW = lc.Weight
		})
	}
}

// linkID returns a stable small id per interface name for this process.
func (c *Client) linkID(name string) uint8 {
	if id, ok := c.ids[name]; ok {
		return id
	}
	used := map[uint8]bool{}
	for _, id := range c.ids {
		used[id] = true
	}
	for id := uint8(1); id < 255; id++ {
		if !used[id] {
			c.ids[name] = id
			return id
		}
	}
	return 255
}

func (c *Client) dialBound(it platform.Iface, server netip.AddrPort) (*net.UDPConn, error) {
	d := net.Dialer{Control: platform.BindControl(it.Name, it.Index)}
	if ip := it.PrimaryIPv4(); ip.IsValid() && server.Addr().Is4() {
		d.LocalAddr = &net.UDPAddr{IP: ip.AsSlice()}
	}
	conn, err := d.Dial("udp", server.String())
	if err != nil {
		return nil, err
	}
	uc := conn.(*net.UDPConn)
	uc.SetReadBuffer(4 << 20)
	uc.SetWriteBuffer(4 << 20)
	return uc, nil
}

// onHelloAck brings the tunnel up (or re-addresses it) once the server has
// assigned our address.
func (c *Client) onHelloAck(a proto.HelloAck) {
	prev := c.ack.Swap(&a)
	c.mu.Lock()
	if c.epoch != 0 && c.epoch != a.Epoch {
		c.log.Info("server session was rebuilt; resetting receive window")
		c.engine.Load().ResetRx()
	}
	c.epoch = a.Epoch
	c.mu.Unlock()
	if prev == nil || prev.ClientIP != a.ClientIP || prev.PrefixLen != a.PrefixLen {
		c.log.Info("connected to server", "server", a.ServerName, "version", a.Version, "tunnel_ip", a.ClientIP)
		c.policySent.Store(0)
		go c.applyTunnel()
	}
}

// tick updates weights, link states, reorder hold, and resends policy.
func (c *Client) tick(now time.Time) {
	cfg := c.cfg.Get()
	e := c.engine.Load()
	c.mu.Lock()
	links := make([]*clink, 0, len(c.links))
	for _, l := range c.links {
		links = append(links, l)
	}
	c.mu.Unlock()

	var best time.Duration
	var bestUp, bestDown float64
	upPeak := make(map[*clink]float64, len(links))
	for _, l := range links {
		l.mu.Lock()
		upPeak[l] = l.upRate.Peak
		l.mu.Unlock()
	}
	for _, l := range links {
		l.link.Health(func(h *bond.Health) {
			if h.RTT.Samples > 0 && (best == 0 || h.RTT.SRTT < best) {
				best = h.RTT.SRTT
			}
			bestUp = max(bestUp, upPeak[l])
			bestDown = max(bestDown, h.RxRate.Peak)
		})
	}
	for _, l := range links {
		lc := cfg.Links[l.iface.Name]
		var srtt time.Duration
		var manualCap float64
		var downPeak float64
		var healthy bool
		l.link.Health(func(h *bond.Health) {
			srtt, manualCap, downPeak = h.RTT.SRTT, h.ManualCap, h.RxRate.Peak
			healthy = h.Loss*100 <= cfg.Health.DegradedLossPct
		})
		upCap := estimate(upPeak[l], bestUp, manualCap, healthy && !l.link.Congested())
		if r := l.link.Pacer.Rate(); r > 0 && manualCap == 0 {
			upCap = r // the pacer has learned what this link can carry
		}
		downCap := estimate(downPeak, bestDown, manualCap, healthy)
		// Shown on the dashboard: the larger of what we know about each direction.
		l.link.Health(func(h *bond.Health) { h.CapBps = max(upCap, downCap) })
		w := lc.Weight
		if w <= 0 {
			w = bond.AutoWeight(upCap, srtt, best, cfg.Bonding.LatencyWeighting)
		}
		l.link.SetWeight(w)
		l.downWeight.Store(uint32(min(lc.Weight*10, 65535)))
		l.downCap.Store(uint32(min(downCap/1000, 4e9)))
	}
	switch cfg.Bonding.ReorderMode {
	case "off":
		e.SetReorderHold(0)
	case "fixed":
		e.SetReorderHold(time.Duration(cfg.Bonding.ReorderMs) * time.Millisecond)
	default:
		e.SetReorderHold(e.AutoReorderHold(time.Duration(cfg.Bonding.ReorderMs) * time.Millisecond))
	}
	e.Tick(now)
	if now.UnixNano()-c.policySent.Load() > int64(5*time.Second) && c.ack.Load() != nil {
		pol := c.wirePolicy()
		for _, l := range links {
			if l.helloAcked.Load() {
				f, _ := proto.AppendJSON(nil, proto.TypePolicy, e.Session, l.id, pol)
				l.link.SendRaw(f)
			}
		}
		c.policySent.Store(now.UnixNano())
	}
}

// estimate turns an observed peak rate into a capacity estimate. Healthy links
// get 25% headroom so the scheduler keeps probing them with more traffic until
// they show strain: passive measurement converges on real capacity.
func estimate(peak, best, manual float64, healthy bool) float64 {
	if manual > 0 {
		return manual
	}
	if peak < 1e6 {
		peak = 1e6
	}
	if healthy {
		peak *= 1.25
	}
	if floor := best * 0.25; peak < floor {
		peak = floor
	}
	return peak
}

// ---------------------------------------------------------------- tunnel

func (c *Client) applyTunnel() {
	if c.NoTunnel {
		return
	}
	a := c.ack.Load()
	cfg := c.cfg.Get()
	if a == nil || !cfg.Server.Enabled {
		return
	}
	ip, err1 := netip.ParseAddr(a.ClientIP)
	gw, err2 := netip.ParseAddr(a.GatewayIP)
	if err1 != nil || err2 != nil {
		c.setTunErr("server sent an invalid address")
		return
	}
	mtu := cfg.Tunnel.MTU
	if a.MTU > 0 && a.MTU < mtu {
		mtu = a.MTU
	}
	spec := platform.TunnelSpec{
		Name:       cfg.Tunnel.InterfaceName,
		Addr:       netip.PrefixFrom(ip, a.PrefixLen),
		Gateway:    gw,
		MTU:        mtu,
		RouterMode: cfg.Tunnel.RouterMode,
	}
	if cfg.Tunnel.FullTunnel {
		spec.Routes = platform.FullTunnelRoutes
	} else {
		spec.Routes, _ = config.ParseCIDRs(cfg.Tunnel.Routes)
	}
	if cfg.Tunnel.DNSOverride {
		spec.DNS, _ = netip.ParseAddr(a.DNS)
	}

	c.tunMu.Lock()
	defer c.tunMu.Unlock()
	recreate := c.tun == nil || c.tunSpec.Name != spec.Name || c.tunSpec.MTU != spec.MTU
	if recreate {
		c.closeTunnelLocked()
		t, err := platform.OpenTun(spec.Name, spec.MTU)
		if err != nil {
			c.tunErr = err.Error()
			c.log.Error("cannot create virtual adapter (run as administrator/root)", "err", err)
			return
		}
		c.tun = t
		go func() {
			err := t.ReadLoop(func(pkt []byte) { c.SendPacket(pkt) })
			c.log.Debug("tunnel reader stopped", "err", err)
		}()
	} else if c.tunUndo != nil {
		c.tunUndo()
		c.tunUndo = nil
	}
	spec.Name, spec.Index = c.tun.Name(), c.tun.Index()
	undo, err := platform.ConfigureTunnel(spec)
	c.tunUndo, c.tunSpec = undo, spec
	if err != nil {
		c.tunErr = err.Error()
		c.log.Error("cannot configure virtual adapter", "err", err)
		return
	}
	c.tunErr = ""
	c.log.Info("virtual adapter up", "name", spec.Name, "ip", spec.Addr, "routes", len(spec.Routes), "dns", spec.DNS)
}

func (c *Client) setTunErr(s string) {
	c.tunMu.Lock()
	c.tunErr = s
	c.tunMu.Unlock()
}

func (c *Client) closeTunnel() {
	c.tunMu.Lock()
	c.closeTunnelLocked()
	c.tunMu.Unlock()
}

func (c *Client) closeTunnelLocked() {
	if c.tunUndo != nil {
		c.tunUndo()
		c.tunUndo = nil
	}
	if c.tun != nil {
		c.tun.Close()
		c.tun = nil
		c.log.Info("virtual adapter removed")
	}
}

// ---------------------------------------------------------------- dashboard

type ServerView struct {
	Address   string `json:"address"`
	Resolved  string `json:"resolved,omitempty"`
	Name      string `json:"name,omitempty"`
	Version   string `json:"version,omitempty"`
	Connected bool   `json:"connected"`
	Enabled   bool   `json:"enabled"`
	Error     string `json:"error,omitempty"`
}

type TunnelView struct {
	Up      bool   `json:"up"`
	Name    string `json:"name,omitempty"`
	IP      string `json:"ip,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	MTU     int    `json:"mtu,omitempty"`
	Routes  int    `json:"routes"`
	DNS     string `json:"dns,omitempty"`
	Error   string `json:"error,omitempty"`
}

type IfaceView struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Desc    string   `json:"desc,omitempty"`
	IPv4    []string `json:"ipv4"`
	Up      bool     `json:"up"`
	Status  string   `json:"status"`
	LinkID  uint8    `json:"linkId,omitempty"`
	Label   string   `json:"label,omitempty"`
	Backup  bool     `json:"backup,omitempty"`
	Weight  float64  `json:"weight,omitempty"`
	MaxMbps float64  `json:"maxMbps,omitempty"`
}

type StateView struct {
	Role       string          `json:"role"`
	Name       string          `json:"name"`
	Uptime     float64         `json:"uptime"`
	Server     ServerView      `json:"server"`
	Tunnel     TunnelView      `json:"tunnel"`
	Engine     bond.EngineView `json:"engine"`
	Links      []bond.LinkView `json:"links"`
	Interfaces []IfaceView     `json:"interfaces"`
	Warnings   []string        `json:"warnings"`
}

func (c *Client) Snapshot(history int) StateView {
	cfg := c.cfg.Get()
	e := c.engine.Load()
	v := StateView{
		Role:   "client",
		Name:   c.deviceName(),
		Uptime: time.Since(c.started).Seconds(),
		Engine: e.View(),
		Links:  []bond.LinkView{},
		Server: ServerView{Address: cfg.Server.Address, Enabled: cfg.Server.Enabled},
	}
	if a := c.ack.Load(); a != nil {
		v.Server.Connected = true
		v.Server.Name, v.Server.Version = a.ServerName, a.Version
	}
	c.mu.Lock()
	if c.server.IsValid() {
		v.Server.Resolved = c.server.String()
	}
	v.Server.Error = c.serverErr
	ifaces := c.ifaces
	linksByName := map[string]*clink{}
	for n, l := range c.links {
		linksByName[n] = l
	}
	c.mu.Unlock()

	for _, l := range e.Links() {
		lv := l.View(history)
		if cl, ok := linksByName[l.Name]; ok {
			lv.Local = cl.iface.PrimaryIPv4().String()
			if !cl.helloAcked.Load() && lv.LastError == "" {
				lv.LastError = cl.status()
			}
		}
		v.Links = append(v.Links, lv)
	}
	connected := false
	for _, l := range v.Links {
		if l.State == "up" || l.State == "degraded" {
			connected = true
		}
	}
	v.Server.Connected = v.Server.Connected && connected

	for _, it := range ifaces {
		lc := cfg.Links[it.Name]
		iv := IfaceView{
			Name: it.Name, Kind: it.Kind, Desc: it.Desc, Up: it.Up,
			Status: c.eligibility(cfg, it),
			Label:  lc.Label, Backup: lc.Backup, Weight: lc.Weight, MaxMbps: lc.MaxMbps,
		}
		for _, p := range it.IPv4 {
			iv.IPv4 = append(iv.IPv4, p.String())
		}
		if l, ok := linksByName[it.Name]; ok {
			iv.LinkID = l.id
		}
		if iv.Status == "virtual" {
			continue
		}
		v.Interfaces = append(v.Interfaces, iv)
	}
	sort.SliceStable(v.Interfaces, func(i, j int) bool {
		return statusRank(v.Interfaces[i].Status) < statusRank(v.Interfaces[j].Status)
	})

	c.tunMu.Lock()
	if c.tun != nil {
		v.Tunnel = TunnelView{Up: true, Name: c.tunSpec.Name, IP: c.tunSpec.Addr.String(), Gateway: c.tunSpec.Gateway.String(),
			MTU: c.tunSpec.MTU, Routes: len(c.tunSpec.Routes)}
		if c.tunSpec.DNS.IsValid() {
			v.Tunnel.DNS = c.tunSpec.DNS.String()
		}
	}
	v.Tunnel.Error = c.tunErr
	c.tunMu.Unlock()
	if c.NoTunnel {
		v.Tunnel.Error = ""
	}

	v.Warnings = []string{}
	if cfg.Server.Token == "" && cfg.Server.Address != "" {
		v.Warnings = append(v.Warnings, "No access token set.")
	}
	if cfg.UI.Password == "" && !isLoopbackListen(cfg.UI.Listen) {
		v.Warnings = append(v.Warnings, "The dashboard is reachable from the network without a password.")
	}
	return v
}

func statusRank(s string) int {
	switch s {
	case "bonded":
		return 0
	case "available":
		return 1
	case "disabled":
		return 2
	case "no-ipv4", "down":
		return 3
	}
	return 4
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return (err == nil && ip.IsLoopback()) || host == "localhost"
}

func (c *Client) deviceName() string {
	if n := c.cfg.Get().Name; n != "" {
		return n
	}
	h, _ := os.Hostname()
	if h == "" {
		h = runtime.GOOS
	}
	return h
}

// Backend adapts the client to the dashboard.
type Backend struct{ C *Client }

func (b Backend) Info() any {
	return map[string]any{"role": "client", "version": Version, "os": runtime.GOOS, "arch": runtime.GOARCH, "name": b.C.deviceName()}
}
func (b Backend) State(h int) any { return b.C.Snapshot(h) }
func (b Backend) Config() any     { return b.C.cfg.Get() }
func (b Backend) Schema() any     { return config.Schema(b.C.cfg.Get()) }
func (b Backend) UpdateConfig(raw []byte) error {
	c, err := MergeConfig(b.C.cfg.Clone(), raw)
	if err != nil {
		return err
	}
	return b.C.cfg.Update(c)
}

// MergeConfig applies a (possibly partial) JSON document to cfg. Maps present
// in the document replace the existing map instead of merging into it, so
// removing a per-link override actually removes it.
func MergeConfig(cfg *config.Client, raw []byte) (*config.Client, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	if _, ok := top["links"]; ok {
		cfg.Links = nil
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
func (b Backend) Password() string { return b.C.cfg.Get().UI.Password }
func (b Backend) RefreshInterval() time.Duration {
	return time.Duration(b.C.cfg.Get().UI.RefreshMs) * time.Millisecond
}

func (b Backend) Routes(mux *http.ServeMux) {
	// PATCH a single link's overrides: {"disabled":true,"backup":false,...}
	mux.HandleFunc("PUT /api/x/links/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		var lc config.Link
		if err := json.NewDecoder(r.Body).Decode(&lc); err != nil {
			webui.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		err := b.C.cfg.Mutate(func(c *config.Client) {
			if c.Links == nil {
				c.Links = map[string]config.Link{}
			}
			c.Links[name] = lc
		})
		if err != nil {
			webui.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		webui.WriteJSON(w, http.StatusOK, lc)
	})
	mux.HandleFunc("POST /api/x/reconnect", func(w http.ResponseWriter, r *http.Request) {
		b.C.mu.Lock()
		for name, l := range b.C.links {
			l.close(true)
			delete(b.C.links, name)
		}
		b.C.server = netip.AddrPort{}
		b.C.mu.Unlock()
		b.C.newSession()
		b.C.poke()
		webui.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
}

var _ = fmt.Sprint
